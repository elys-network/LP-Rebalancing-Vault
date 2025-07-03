package planner

import (
	"errors"
	"fmt"
	"math"
	"sort"

	sdkmath "cosmossdk.io/math"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/simulations"
	"github.com/elys-network/avm/internal/types"
	"github.com/elys-network/avm/internal/utils"
	"github.com/rs/zerolog"
)

// Error definitions for zero-tolerance error handling
var (
	ErrInvalidVaultValue        = errors.New("vault value must be positive")
	ErrInvalidLiquidUSDC        = errors.New("liquid USDC cannot be negative")
	ErrInvalidTargetAllocations = errors.New("target allocations contain invalid values")
	ErrMissingPoolData          = errors.New("required pool data is missing")
	ErrMissingTokenData         = errors.New("required token data is missing")
	ErrInvalidScoringParams     = errors.New("scoring parameters contain invalid values")
	ErrUSDCNotFound             = errors.New("USDC token configuration not found")
	ErrInvalidSlippageLimit     = errors.New("slippage limit is invalid")
	ErrSimulationFailed         = errors.New("simulation failed")
	ErrInsufficientFunds        = errors.New("insufficient funds for operation")
	ErrInvalidPoolState         = errors.New("pool state is invalid for operation")
	ErrMathematicalError        = errors.New("mathematical calculation error")
)

// ExtendedAction represents a high-level action with additional metadata
type ExtendedAction struct {
	PoolID         types.PoolID
	DeltaUSD       float64
	TargetLPShares sdkmath.Int
}

// GenerateActionPlan creates a strategic action plan for vault rebalancing using single-sided deposits
// Returns two separate action plans: withdrawals/consolidation, then single-sided deposits
func GenerateActionPlan(
	currentPositions []types.Position,
	initialLiquidUSDC float64,
	targetAllocations map[types.PoolID]float64,
	totalVaultValueUSD float64,
	poolsData map[types.PoolID]types.Pool,
	tokenDataMap map[string]types.Token,
	scoringParams types.ScoringParameters,
	tendermintRPCEndpoint string,
) (withdrawalActions []types.SubAction, depositActions []types.SubAction, err error) {
	actionLogger := logger.GetForComponent("action_planner")

	// ===== COMPREHENSIVE INPUT VALIDATION =====
	if err := validateInputs(currentPositions, initialLiquidUSDC, targetAllocations, totalVaultValueUSD,
		poolsData, tokenDataMap, scoringParams, tendermintRPCEndpoint); err != nil {
		actionLogger.Error().Err(err).Msg("Input validation failed")
		return nil, nil, err
	}

	// Handle edge case where vault is empty
	if totalVaultValueUSD <= 0 && initialLiquidUSDC <= 0 {
		actionLogger.Info().Msg("Total vault value and liquid USDC are zero, no actions to plan")
		return []types.SubAction{}, []types.SubAction{}, nil
	}

	if totalVaultValueUSD <= 0 && initialLiquidUSDC > 0 {
		totalVaultValueUSD = initialLiquidUSDC
		actionLogger.Info().Float64("totalVaultValueUSD", totalVaultValueUSD).Msg("Vault was empty, using liquid USDC as total value")
	}

	// ===== USDC CONFIGURATION VALIDATION =====
	usdcToken, err := validateAndGetUSDCToken(tokenDataMap)
	if err != nil {
		actionLogger.Error().Err(err).Msg("USDC token validation failed")
		return nil, nil, err
	}

	actionLogger.Info().
		Str("usdcDenom", usdcToken.IBCDenom).
		Int("usdcPrecision", usdcToken.Precision).
		Msg("USDC configuration validated")

	// ===== INITIALIZE TRACKING VARIABLES =====
	simulatedLiquidUSDC := initialLiquidUSDC
	actionLogger.Info().Float64("initialLiquidUSDC", initialLiquidUSDC).Msg("Initial liquid USDC for planning")

	// ===== ANALYZE HIGH-LEVEL CHANGES =====
	highLevelWithdrawals, highLevelDeposits, err := analyzeRequiredChanges(
		currentPositions, targetAllocations, totalVaultValueUSD, poolsData, scoringParams)
	if err != nil {
		actionLogger.Error().Err(err).Msg("Failed to analyze required changes")
		return nil, nil, err
	}

	// ===== APPLY REBALANCING LIMITS =====
	highLevelWithdrawals, highLevelDeposits = applyRebalancingLimits(
		highLevelWithdrawals, highLevelDeposits, totalVaultValueUSD, scoringParams, actionLogger)

	actionLogger.Info().
		Int("withdrawals", len(highLevelWithdrawals)).
		Int("deposits", len(highLevelDeposits)).
		Msg("High-level action analysis complete")

	// ===== PROCESS WITHDRAWALS =====
	withdrawalActions, newLiquidUSDC, tempNonUSDCAssets, err := processWithdrawals(
		highLevelWithdrawals, currentPositions, poolsData, simulatedLiquidUSDC,
		usdcToken, tendermintRPCEndpoint, scoringParams)
	if err != nil {
		actionLogger.Error().Err(err).Msg("Withdrawal processing failed")
		return nil, nil, err
	}
	simulatedLiquidUSDC = newLiquidUSDC

	// ===== CONSOLIDATE NON-USDC ASSETS =====
	consolidationActions, finalLiquidUSDC, err := processConsolidation(
		tempNonUSDCAssets, simulatedLiquidUSDC, usdcToken, tendermintRPCEndpoint, scoringParams)
	if err != nil {
		actionLogger.Error().Err(err).Msg("Asset consolidation failed")
		return nil, nil, err
	}
	simulatedLiquidUSDC = finalLiquidUSDC

	// Append consolidation swaps to withdrawal actions
	withdrawalActions = append(withdrawalActions, consolidationActions...)

	// ===== PROCESS DEPOSITS =====
	depositActions, err = processDeposits(
		highLevelDeposits, poolsData, simulatedLiquidUSDC, usdcToken,
		tendermintRPCEndpoint, scoringParams)
	if err != nil {
		actionLogger.Error().Err(err).Msg("Deposit processing failed")
		return nil, nil, err
	}

	actionLogger.Info().
		Int("withdrawalsAndConsolidation", len(withdrawalActions)).
		Int("singleSidedDeposits", len(depositActions)).
		Msg("Action plan generation completed successfully")

	return withdrawalActions, depositActions, nil
}

// validateInputs performs comprehensive validation of all input parameters
func validateInputs(
	currentPositions []types.Position,
	initialLiquidUSDC float64,
	targetAllocations map[types.PoolID]float64,
	totalVaultValueUSD float64,
	poolsData map[types.PoolID]types.Pool,
	tokenDataMap map[string]types.Token,
	scoringParams types.ScoringParameters,
	tendermintRPCEndpoint string,
) error {
	// Validate liquid USDC
	if math.IsNaN(initialLiquidUSDC) || math.IsInf(initialLiquidUSDC, 0) {
		return errors.Join(ErrInvalidLiquidUSDC, errors.New("initial liquid USDC is not finite"))
	}
	if initialLiquidUSDC < 0 {
		return errors.Join(ErrInvalidLiquidUSDC, errors.New("initial liquid USDC cannot be negative"))
	}

	// Validate total vault value
	if math.IsNaN(totalVaultValueUSD) || math.IsInf(totalVaultValueUSD, 0) {
		return errors.Join(ErrInvalidVaultValue, errors.New("total vault value is not finite"))
	}

	// Validate target allocations
	if targetAllocations == nil {
		return errors.Join(ErrInvalidTargetAllocations, errors.New("target allocations map is nil"))
	}

	totalAllocation := 0.0
	for poolID, allocation := range targetAllocations {
		if math.IsNaN(allocation) || math.IsInf(allocation, 0) {
			return errors.Join(ErrInvalidTargetAllocations,
				fmt.Errorf("allocation for pool %d is not finite: %f", poolID, allocation))
		}
		if allocation < 0 {
			return errors.Join(ErrInvalidTargetAllocations,
				fmt.Errorf("allocation for pool %d is negative: %f", poolID, allocation))
		}
		if allocation > 1 {
			return errors.Join(ErrInvalidTargetAllocations,
				fmt.Errorf("allocation for pool %d exceeds 100%%: %f", poolID, allocation))
		}
		totalAllocation += allocation
	}

	if math.Abs(totalAllocation-1.0) > 0.01 {
		return errors.Join(ErrInvalidTargetAllocations,
			fmt.Errorf("total allocations (%.6f) do not sum to 1.0", totalAllocation))
	}

	// Validate current positions
	for i, pos := range currentPositions {
		if pos.PoolID == 0 {
			return fmt.Errorf("position %d has invalid pool ID", i)
		}
		if pos.LPShares.IsNil() || pos.LPShares.IsNegative() {
			return fmt.Errorf("position %d has invalid LP shares", i)
		}
		if math.IsNaN(pos.EstimatedValue) || math.IsInf(pos.EstimatedValue, 0) {
			return fmt.Errorf("position %d has invalid estimated value", i)
		}
		if pos.EstimatedValue < 0 {
			return fmt.Errorf("position %d has negative estimated value", i)
		}
	}

	// Validate pools data
	if poolsData == nil {
		return errors.Join(ErrMissingPoolData, errors.New("pool data map is nil"))
	}

	// Validate token data
	if tokenDataMap == nil {
		return errors.Join(ErrMissingTokenData, errors.New("token data map is nil"))
	}
	if len(tokenDataMap) == 0 {
		return errors.Join(ErrMissingTokenData, errors.New("token data map is empty"))
	}

	// Validate scoring parameters
	if err := validateScoringParams(scoringParams); err != nil {
		return errors.Join(ErrInvalidScoringParams, err)
	}

	// Validate RPC endpoint
	if tendermintRPCEndpoint == "" {
		return errors.New("tendermint RPC endpoint cannot be empty")
	}

	return nil
}

// validateScoringParams validates scoring parameters for financial safety
func validateScoringParams(params types.ScoringParameters) error {
	if math.IsNaN(params.RebalanceThresholdAmount) || math.IsInf(params.RebalanceThresholdAmount, 0) {
		return errors.New("rebalance threshold is not finite")
	}
	if params.RebalanceThresholdAmount < 0 {
		return errors.New("rebalance threshold cannot be negative")
	}
	if params.RebalanceThresholdAmount > 100 {
		return errors.New("rebalance threshold cannot exceed 100%")
	}

	if math.IsNaN(params.MaxRebalancePercentPerCycle) || math.IsInf(params.MaxRebalancePercentPerCycle, 0) {
		return errors.New("max rebalance percent per cycle is not finite")
	}
	if params.MaxRebalancePercentPerCycle < 0 {
		return errors.New("max rebalance percent per cycle cannot be negative")
	}
	if params.MaxRebalancePercentPerCycle > 100 {
		return errors.New("max rebalance percent per cycle cannot exceed 100%")
	}

	if math.IsNaN(params.SmartShieldSlippagePercent) || math.IsInf(params.SmartShieldSlippagePercent, 0) {
		return errors.New("smart shield slippage percent is not finite")
	}
	if params.SmartShieldSlippagePercent < 0 || params.SmartShieldSlippagePercent > 100 {
		return errors.New("smart shield slippage percent must be between 0 and 100")
	}

	if math.IsNaN(params.NormalPoolSlippagePercent) || math.IsInf(params.NormalPoolSlippagePercent, 0) {
		return errors.New("normal pool slippage percent is not finite")
	}
	if params.NormalPoolSlippagePercent < 0 || params.NormalPoolSlippagePercent > 100 {
		return errors.New("normal pool slippage percent must be between 0 and 100")
	}

	if math.IsNaN(params.ViableSwapReductionFactor) || math.IsInf(params.ViableSwapReductionFactor, 0) {
		return errors.New("viable swap reduction factor is not finite")
	}
	if params.ViableSwapReductionFactor <= 0 || params.ViableSwapReductionFactor > 1 {
		return errors.New("viable swap reduction factor must be between 0 and 1")
	}

	if math.IsNaN(params.ViableDepositReductionFactor) || math.IsInf(params.ViableDepositReductionFactor, 0) {
		return errors.New("viable deposit reduction factor is not finite")
	}
	if params.ViableDepositReductionFactor <= 0 || params.ViableDepositReductionFactor > 1 {
		return errors.New("viable deposit reduction factor must be between 0 and 1")
	}

	return nil
}

// validateAndGetUSDCToken validates and returns USDC token configuration
func validateAndGetUSDCToken(tokenDataMap map[string]types.Token) (types.Token, error) {
	usdcToken, found := types.Token{}, false
	for _, token := range tokenDataMap {
		if token.Symbol == "USDC" {
			usdcToken = token
			found = true
			break
		}
	}

	if !found {
		return types.Token{}, errors.Join(ErrUSDCNotFound,
			errors.New("USDC token not found in token data map"))
	}

	if usdcToken.IBCDenom == "" {
		return types.Token{}, errors.Join(ErrUSDCNotFound,
			errors.New("USDC IBCDenom is empty"))
	}

	if usdcToken.Precision < 0 || usdcToken.Precision > 18 {
		return types.Token{}, errors.Join(ErrUSDCNotFound,
			fmt.Errorf("USDC precision is invalid: %d", usdcToken.Precision))
	}

	if math.IsNaN(usdcToken.PriceUSD) || math.IsInf(usdcToken.PriceUSD, 0) {
		return types.Token{}, errors.Join(ErrUSDCNotFound,
			errors.New("USDC price is not finite"))
	}

	// USDC should be approximately $1
	if math.Abs(usdcToken.PriceUSD-1.0) > 0.1 {
		return types.Token{}, errors.Join(ErrUSDCNotFound,
			fmt.Errorf("USDC price is too far from $1: %f", usdcToken.PriceUSD))
	}

	return usdcToken, nil
}

// analyzeRequiredChanges determines which pools need withdrawals vs deposits
func analyzeRequiredChanges(
	currentPositions []types.Position,
	targetAllocations map[types.PoolID]float64,
	totalVaultValueUSD float64,
	poolsData map[types.PoolID]types.Pool,
	scoringParams types.ScoringParameters,
) ([]ExtendedAction, []ExtendedAction, error) {

	actionLogger := logger.GetForComponent("action_planner")

	var withdrawals []ExtendedAction
	var deposits []ExtendedAction

	// Get all pools involved (current positions + target allocations)
	allPoolIDs := make(map[types.PoolID]struct{})
	for _, pos := range currentPositions {
		allPoolIDs[pos.PoolID] = struct{}{}
	}
	for poolID := range targetAllocations {
		allPoolIDs[poolID] = struct{}{}
	}

	for poolID := range allPoolIDs {
		// Get current USD value
		currentUSD := 0.0
		currentPos, hasPosition := findPosition(currentPositions, poolID)
		if hasPosition {
			currentUSD = currentPos.EstimatedValue
		}

		// Get target USD value
		targetAllocation, hasTarget := targetAllocations[poolID]
		if !hasTarget {
			targetAllocation = 0.0
		}
		targetUSD := totalVaultValueUSD * targetAllocation

		// Validate calculations
		if math.IsNaN(currentUSD) || math.IsInf(currentUSD, 0) {
			return nil, nil, fmt.Errorf("current USD value for pool %d is not finite", poolID)
		}
		if math.IsNaN(targetUSD) || math.IsInf(targetUSD, 0) {
			return nil, nil, fmt.Errorf("target USD value for pool %d is not finite", poolID)
		}

		deltaUSD := targetUSD - currentUSD

		// Calculate percentage deviation
		deltaPercentage := 0.0
		if targetUSD > 0 {
			deltaPercentage = (deltaUSD / targetUSD) * 100.0
		} else if currentUSD > 0 {
			deltaPercentage = -100.0 // Complete exit
		}

		// Validate percentage calculation
		if math.IsNaN(deltaPercentage) || math.IsInf(deltaPercentage, 0) {
			return nil, nil, fmt.Errorf("delta percentage calculation failed for pool %d", poolID)
		}

		actionLogger.Debug().
			Uint64("poolID", uint64(poolID)).
			Float64("currentUSD", currentUSD).
			Float64("targetUSD", targetUSD).
			Float64("deltaUSD", deltaUSD).
			Float64("deltaPercentage", deltaPercentage).
			Float64("thresholdPercent", scoringParams.RebalanceThresholdAmount).
			Msg("Pool rebalancing analysis")

		if deltaPercentage < -scoringParams.RebalanceThresholdAmount {
			// Need to withdraw
			targetShares, err := calculateTargetShares(targetUSD, poolID, poolsData)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to calculate target shares for withdrawal from pool %d: %w", poolID, err)
			}

			withdrawals = append(withdrawals, ExtendedAction{
				PoolID:         poolID,
				DeltaUSD:       deltaUSD,
				TargetLPShares: targetShares,
			})
		} else if deltaPercentage > scoringParams.RebalanceThresholdAmount {
			// Need to deposit
			targetShares, err := calculateTargetShares(targetUSD, poolID, poolsData)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to calculate target shares for deposit to pool %d: %w", poolID, err)
			}

			deposits = append(deposits, ExtendedAction{
				PoolID:         poolID,
				DeltaUSD:       deltaUSD,
				TargetLPShares: targetShares,
			})
		}
	}

	return withdrawals, deposits, nil
}

// calculateTargetShares calculates target LP shares for a given USD amount
func calculateTargetShares(targetUSD float64, poolID types.PoolID, poolsData map[types.PoolID]types.Pool) (sdkmath.Int, error) {
	if targetUSD <= 0 {
		return sdkmath.ZeroInt(), nil
	}

	poolInfo, exists := poolsData[poolID]
	if !exists {
		return sdkmath.ZeroInt(), fmt.Errorf("pool %d data not found", poolID)
	}

	if poolInfo.TotalShares.IsZero() {
		return sdkmath.ZeroInt(), fmt.Errorf("pool %d has zero total shares", poolID)
	}

	if poolInfo.TvlUSD <= 0 {
		return sdkmath.ZeroInt(), fmt.Errorf("pool %d has invalid TVL: %f", poolID, poolInfo.TvlUSD)
	}

	shareRatio := targetUSD / poolInfo.TvlUSD
	if math.IsNaN(shareRatio) || math.IsInf(shareRatio, 0) {
		return sdkmath.ZeroInt(), fmt.Errorf("share ratio calculation failed for pool %d", poolID)
	}

	// if shareRatio < 0 || shareRatio > 1 {
	// 	return sdkmath.ZeroInt(), fmt.Errorf("share ratio out of bounds for pool %d: %f", poolID, shareRatio)
	// }

	targetShares := sdkmath.LegacyMustNewDecFromStr(fmt.Sprintf("%f", shareRatio)).
		Mul(sdkmath.LegacyNewDecFromInt(poolInfo.TotalShares)).
		TruncateInt()

	return targetShares, nil
}

// processWithdrawals handles all withdrawal operations
func processWithdrawals(
	withdrawals []ExtendedAction,
	currentPositions []types.Position,
	poolsData map[types.PoolID]types.Pool,
	simulatedLiquidUSDC float64,
	usdcToken types.Token,
	rpcEndpoint string,
	scoringParams types.ScoringParameters,
) ([]types.SubAction, float64, map[string]sdkmath.Int, error) {

	actionLogger := logger.GetForComponent("action_planner")

	var actions []types.SubAction
	tempNonUSDCAssets := make(map[string]sdkmath.Int)

	// Sort by largest withdrawal first
	sort.Slice(withdrawals, func(i, j int) bool {
		return withdrawals[i].DeltaUSD < withdrawals[j].DeltaUSD
	})

	for _, withdrawal := range withdrawals {
		currentPos, hasPosition := findPosition(currentPositions, withdrawal.PoolID)
		if !hasPosition {
			actionLogger.Warn().Uint64("poolID", uint64(withdrawal.PoolID)).
				Msg("No current position found for withdrawal, skipping")
			continue
		}

		sharesToWithdraw := currentPos.LPShares.Sub(withdrawal.TargetLPShares)
		if sharesToWithdraw.IsZero() || sharesToWithdraw.IsNegative() {
			actionLogger.Debug().Uint64("poolID", uint64(withdrawal.PoolID)).
				Msg("No shares to withdraw, skipping")
			continue
		}

		// Validate pool exists and is withdrawable
		poolInfo, poolExists := poolsData[withdrawal.PoolID]
		if !poolExists {
			return nil, 0, nil, fmt.Errorf("pool %d data missing for withdrawal", withdrawal.PoolID)
		}

		// Simulate withdrawal with proper error handling
		exitEst, err := simulations.SimulateLeavePool(rpcEndpoint, uint64(withdrawal.PoolID),
			sharesToWithdraw, usdcToken.IBCDenom)
		if err != nil {
			return nil, 0, nil, errors.Join(ErrSimulationFailed,
				fmt.Errorf("failed to simulate leave pool %d: %w", withdrawal.PoolID, err))
		}

		// Validate simulation result
		if err := validateExitEstimation(exitEst, withdrawal.PoolID); err != nil {
			return nil, 0, nil, err
		}

		// Check slippage tolerance
		maxSlippage := getSlippageLimit(poolInfo, scoringParams)
		if exitEst.Slippage > maxSlippage {
			actionLogger.Warn().
				Uint64("poolID", uint64(withdrawal.PoolID)).
				Float64("slippage", exitEst.Slippage).
				Float64("maxSlippage", maxSlippage).
				Msg("Exit slippage exceeds limit, but proceeding for rebalancing")
		}

		// Create withdrawal action with slippage protection
		actions = append(actions, types.SubAction{
			Type:                 types.SubActionWithdrawLP,
			PoolIDToWithdraw:     withdrawal.PoolID,
			LPSharesToWithdraw:   sharesToWithdraw,
			TargetDenomOnExit:    usdcToken.IBCDenom,
			ExpectedAmountsOut:   exitEst.AmountsOut,
			ExpectedSlippage:     exitEst.Slippage,
			SlippageTolerancePct: maxSlippage,
		})

		// Track received assets
		for _, coinOut := range exitEst.AmountsOut {
			if coinOut.Denom == usdcToken.IBCDenom {
				usdcReceived, err := utils.SDKIntToFloat64(coinOut.Amount, usdcToken.Precision)
				if err != nil {
					return nil, 0, nil, fmt.Errorf("failed to convert USDC amount: %w", err)
				}
				simulatedLiquidUSDC += usdcReceived
			} else {
				// Track non-USDC assets for later consolidation
				if existing, exists := tempNonUSDCAssets[coinOut.Denom]; exists {
					tempNonUSDCAssets[coinOut.Denom] = existing.Add(coinOut.Amount)
				} else {
					tempNonUSDCAssets[coinOut.Denom] = coinOut.Amount
				}
			}
		}

		actionLogger.Info().
			Uint64("poolID", uint64(withdrawal.PoolID)).
			Str("sharesToWithdraw", sharesToWithdraw.String()).
			Float64("slippage", exitEst.Slippage).
			Msg("Withdrawal action created")
	}

	return actions, simulatedLiquidUSDC, tempNonUSDCAssets, nil
}

// processConsolidation handles consolidation of non-USDC assets
func processConsolidation(
	tempNonUSDCAssets map[string]sdkmath.Int,
	simulatedLiquidUSDC float64,
	usdcToken types.Token,
	rpcEndpoint string,
	scoringParams types.ScoringParameters,
) ([]types.SubAction, float64, error) {

	actionLogger := logger.GetForComponent("action_planner")

	var actions []types.SubAction

	if len(tempNonUSDCAssets) == 0 {
		return actions, simulatedLiquidUSDC, nil
	}

	actionLogger.Info().Int("assetsToConsolidate", len(tempNonUSDCAssets)).
		Msg("Processing asset consolidation")

	maxSlippage := scoringParams.NormalPoolSlippagePercent / 100.0

	for denom, amount := range tempNonUSDCAssets {
		if amount.IsZero() {
			continue
		}

		if denom == usdcToken.IBCDenom {
			return nil, 0, fmt.Errorf("found USDC in non-USDC assets map: %s", denom)
		}

		// Try to swap with reduced amounts if slippage is too high
		swapEst, finalAmount, err := findViableSwapAmount(rpcEndpoint, amount, denom,
			usdcToken.IBCDenom, maxSlippage, scoringParams.ViableSwapReductionFactor)
		if err != nil {
			actionLogger.Error().Err(err).Str("denom", denom).
				Msg("Failed to find viable swap amount, skipping consolidation")
			continue
		}

		if finalAmount.IsZero() {
			actionLogger.Warn().Str("denom", denom).
				Msg("No viable swap amount found, skipping")
			continue
		}

		actions = append(actions, types.SubAction{
			Type:                 types.SubActionSwap,
			TokenIn:              sdktypes.NewCoin(denom, finalAmount),
			TokenOutDenom:        usdcToken.IBCDenom,
			ExpectedTokenOut:     swapEst.TokenOutAmount,
			ExpectedSlippage:     swapEst.Slippage,
			SlippageTolerancePct: maxSlippage,
		})

		usdcReceived, err := utils.SDKIntToFloat64(swapEst.TokenOutAmount, usdcToken.Precision)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to convert consolidated USDC: %w", err)
		}
		simulatedLiquidUSDC += usdcReceived

		actionLogger.Info().
			Str("fromDenom", denom).
			Str("amount", finalAmount.String()).
			Float64("slippage", swapEst.Slippage).
			Msg("Consolidation swap created")
	}

	return actions, simulatedLiquidUSDC, nil
}

// processDeposits handles all deposit operations
func processDeposits(
	deposits []ExtendedAction,
	poolsData map[types.PoolID]types.Pool,
	simulatedLiquidUSDC float64,
	usdcToken types.Token,
	rpcEndpoint string,
	scoringParams types.ScoringParameters,
) ([]types.SubAction, error) {

	actionLogger := logger.GetForComponent("action_planner")

	var actions []types.SubAction

	// Sort by largest deposit first
	sort.Slice(deposits, func(i, j int) bool {
		return deposits[i].DeltaUSD > deposits[j].DeltaUSD
	})

	for _, deposit := range deposits {
		poolInfo, poolExists := poolsData[deposit.PoolID]
		if !poolExists {
			return nil, fmt.Errorf("pool %d data missing for deposit", deposit.PoolID)
		}

		targetUSDCAmount := deposit.DeltaUSD
		if targetUSDCAmount <= 0 {
			actionLogger.Debug().Uint64("poolID", uint64(deposit.PoolID)).
				Msg("Target deposit amount is zero or negative, skipping")
			continue
		}

		// Always ensure we maintain the minimum liquid USDC buffer
		maxAvailableForDeposit := simulatedLiquidUSDC - scoringParams.MinLiquidUSDCBuffer
		if maxAvailableForDeposit < 0 {
			maxAvailableForDeposit = 0
		}

		if targetUSDCAmount > maxAvailableForDeposit {
			actionLogger.Warn().
				Uint64("poolID", uint64(deposit.PoolID)).
				Float64("needed", targetUSDCAmount).
				Float64("available", simulatedLiquidUSDC).
				Float64("maxAvailable", maxAvailableForDeposit).
				Float64("buffer", scoringParams.MinLiquidUSDCBuffer).
				Msg("Reducing deposit amount to maintain minimum liquid USDC buffer")
			targetUSDCAmount = maxAvailableForDeposit
		}

		if targetUSDCAmount < 1.0 {
			actionLogger.Debug().Uint64("poolID", uint64(deposit.PoolID)).
				Msg("Target amount too small (dust), skipping")
			continue
		}

		usdcAmount, err := utils.Float64ToSDKInt(targetUSDCAmount, usdcToken.Precision)
		if err != nil {
			return nil, fmt.Errorf("failed to convert USDC amount for pool %d: %w", deposit.PoolID, err)
		}

		amountsIn := []sdktypes.Coin{{
			Denom:  usdcToken.IBCDenom,
			Amount: usdcAmount,
		}}

		// Find viable deposit amount considering slippage
		joinEst, finalAmount, err := findViableDepositAmount(rpcEndpoint, uint64(deposit.PoolID),
			amountsIn, poolInfo, scoringParams)
		if err != nil {
			actionLogger.Error().Err(err).Uint64("poolID", uint64(deposit.PoolID)).
				Msg("Failed to find viable deposit amount, skipping")
			continue
		}

		if finalAmount.IsZero() {
			actionLogger.Warn().Uint64("poolID", uint64(deposit.PoolID)).
				Msg("No viable deposit amount found, skipping")
			continue
		}

		maxSlippage := getSlippageLimit(poolInfo, scoringParams)

		actions = append(actions, types.SubAction{
			Type:            types.SubActionDepositLP,
			PoolIDToDeposit: deposit.PoolID,
			AmountsToDeposit: []sdktypes.Coin{{
				Denom:  usdcToken.IBCDenom,
				Amount: finalAmount,
			}},
			ExpectedSharesOut:    joinEst.ShareAmountOut.Amount,
			ExpectedSlippage:     joinEst.Slippage,
			SlippageTolerancePct: maxSlippage,
		})

		// Update liquid USDC
		usedUSDC, err := utils.SDKIntToFloat64(finalAmount, usdcToken.Precision)
		if err != nil {
			return nil, fmt.Errorf("failed to convert used USDC amount: %w", err)
		}
		simulatedLiquidUSDC -= usedUSDC

		actionLogger.Info().
			Uint64("poolID", uint64(deposit.PoolID)).
			Float64("usdcAmount", usedUSDC).
			Float64("slippage", joinEst.Slippage).
			Msg("Deposit action created")
	}

	return actions, nil
}

// Helper functions with proper error handling

func validateExitEstimation(exitEst simulations.ExitPoolEstimationResult, poolID types.PoolID) error {
	if math.IsNaN(exitEst.Slippage) || math.IsInf(exitEst.Slippage, 0) {
		return fmt.Errorf("exit estimation slippage is not finite for pool %d", poolID)
	}
	if exitEst.Slippage < 0 {
		return fmt.Errorf("exit estimation slippage is negative for pool %d", poolID)
	}
	if len(exitEst.AmountsOut) == 0 {
		return fmt.Errorf("exit estimation returned no amounts for pool %d", poolID)
	}
	return nil
}

func findViableSwapAmount(
	rpcEndpoint string,
	maxAmount sdkmath.Int,
	fromDenom, toDenom string,
	maxSlippage float64,
	reductionFactor float64,
) (simulations.SwapEstimationResult, sdkmath.Int, error) {

	maxRetries := 20
	currentAmount := maxAmount

	for retry := 0; retry < maxRetries; retry++ {
		if currentAmount.IsZero() {
			break
		}

		swapEst, err := simulations.SimulateSwap(rpcEndpoint, currentAmount, fromDenom, toDenom)
		if err == nil && swapEst.Slippage <= maxSlippage {
			return swapEst, currentAmount, nil
		}

		// Reduce amount
		currentAmountFloat, err := utils.SDKIntToFloat64(currentAmount, 18) // Assume 18 decimals
		if err != nil {
			break
		}
		currentAmountFloat *= reductionFactor
		currentAmount, err = utils.Float64ToSDKInt(currentAmountFloat, 18)
		if err != nil {
			break
		}
	}

	return simulations.SwapEstimationResult{}, sdkmath.ZeroInt(),
		errors.New("no viable swap amount found within slippage tolerance")
}

func findViableDepositAmount(
	rpcEndpoint string,
	poolID uint64,
	amountsIn []sdktypes.Coin,
	poolInfo types.Pool,
	scoringParams types.ScoringParameters,
) (simulations.JoinPoolEstimationResult, sdkmath.Int, error) {

	maxSlippage := getSlippageLimit(poolInfo, scoringParams)

	// Try original amount first
	joinEst, err := simulations.SimulateJoinPool(rpcEndpoint, poolID, amountsIn)
	if err == nil && joinEst.Slippage <= maxSlippage {
		return joinEst, amountsIn[0].Amount, nil
	}

	// Try reduced amount using the configured reduction factor
	reductionFactorInt := sdkmath.NewInt(int64(scoringParams.ViableDepositReductionFactor * 100))
	reducedAmount := amountsIn[0].Amount.Mul(reductionFactorInt).Quo(sdkmath.NewInt(100))
	reducedAmountsIn := []sdktypes.Coin{{
		Denom:  amountsIn[0].Denom,
		Amount: reducedAmount,
	}}

	joinEst, err = simulations.SimulateJoinPool(rpcEndpoint, poolID, reducedAmountsIn)
	if err == nil && joinEst.Slippage <= maxSlippage {
		return joinEst, reducedAmount, nil
	}

	return simulations.JoinPoolEstimationResult{}, sdkmath.ZeroInt(),
		errors.New("no viable deposit amount found within slippage tolerance")
}

func getSlippageLimit(pool types.Pool, scoringParams types.ScoringParameters) float64 {
	if pool.IsSmartShielded {
		return scoringParams.SmartShieldSlippagePercent / 100.0
	}
	return scoringParams.NormalPoolSlippagePercent / 100.0
}

func findPosition(positions []types.Position, poolID types.PoolID) (types.Position, bool) {
	for _, pos := range positions {
		if pos.PoolID == poolID {
			return pos, true
		}
	}
	return types.Position{}, false
}

// applyRebalancingLimits caps the total amount that can be withdrawn from pools per cycle
// Only limits withdrawals, not deposits, as withdrawals trigger higher trading costs and slippage
func applyRebalancingLimits(
	withdrawals []ExtendedAction,
	deposits []ExtendedAction,
	totalVaultValueUSD float64,
	scoringParams types.ScoringParameters,
	actionLogger zerolog.Logger,
) ([]ExtendedAction, []ExtendedAction) {
	
	maxWithdrawalUSD := totalVaultValueUSD * (scoringParams.MaxRebalancePercentPerCycle / 100.0)
	
	actionLogger.Info().
		Float64("maxWithdrawalUSD", maxWithdrawalUSD).
		Float64("maxWithdrawalPercent", scoringParams.MaxRebalancePercentPerCycle).
		Msg("Applying withdrawal limits (deposits unlimited)")

	// Calculate total withdrawal amount
	totalWithdrawalUSD := 0.0
	for _, w := range withdrawals {
		totalWithdrawalUSD += math.Abs(w.DeltaUSD) // DeltaUSD is negative for withdrawals
	}

	// Deposits are not limited - they represent new opportunities with lower trading costs
	if totalWithdrawalUSD <= maxWithdrawalUSD {
		actionLogger.Info().
			Float64("totalWithdrawalUSD", totalWithdrawalUSD).
			Msg("Withdrawal amount within limits, no capping needed")
		return withdrawals, deposits
	}

	// Calculate scaling factor for withdrawals only
	scalingFactor := maxWithdrawalUSD / totalWithdrawalUSD
	
	actionLogger.Warn().
		Float64("totalWithdrawalUSD", totalWithdrawalUSD).
		Float64("scalingFactor", scalingFactor).
		Msg("Withdrawal amount exceeds limit, scaling down withdrawal actions only")

	// Scale down withdrawals
	cappedWithdrawals := make([]ExtendedAction, len(withdrawals))
	for i, w := range withdrawals {
		cappedWithdrawals[i] = ExtendedAction{
			PoolID:         w.PoolID,
			DeltaUSD:       w.DeltaUSD * scalingFactor, // DeltaUSD is negative, so this scales the magnitude
			TargetLPShares: w.TargetLPShares, // Will be recalculated based on scaled DeltaUSD
		}
	}

	// Deposits remain unchanged - no limits on new investments
	actionLogger.Info().
		Int("originalWithdrawals", len(withdrawals)).
		Int("cappedWithdrawals", len(cappedWithdrawals)).
		Int("depositsUnchanged", len(deposits)).
		Float64("totalCappedWithdrawals", maxWithdrawalUSD).
		Msg("Applied withdrawal limits - deposits unlimited")

	return cappedWithdrawals, deposits
}

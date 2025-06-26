package avm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/elys-network/avm/internal/analyzer"
	"github.com/elys-network/avm/internal/config"
	datafetcher "github.com/elys-network/avm/internal/datafetcher"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/planner"
	"github.com/elys-network/avm/internal/state"
	"github.com/elys-network/avm/internal/types"
	"github.com/elys-network/avm/internal/vault"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
)

const (
	// Export constants for use in main.go
	DEFAULT_SCORING_CONFIG_NAME    = "default_avm_strategy"
	DEFAULT_SCORING_CONFIG_VERSION = 1
)

// AVM represents the Autonomous Vault Manager with all its dependencies
type AVM struct {
	// Core dependencies
	logger       zerolog.Logger
	vault        vault.VaultManager
	grpcClient   *grpc.ClientConn
	scoringParams *types.ScoringParameters
	
	// Configuration
	configName    string
	configVersion int
	
	// Runtime state
	cycleCount int
}

// Config holds the configuration for creating a new AVM instance
type Config struct {
	GRPCClient     *grpc.ClientConn
	VaultManager   vault.VaultManager
	ScoringParams  *types.ScoringParameters
	ConfigName     string
	ConfigVersion  int
}

// NewAVM creates a new AVM instance with dependency injection
func NewAVM(cfg Config) (*AVM, error) {
	// Validate required dependencies
	if err := validateAVMConfig(cfg); err != nil {
		return nil, fmt.Errorf("AVM configuration validation failed: %w", err)
	}

	// Create AVM instance
	avm := &AVM{
		logger:        logger.GetForComponent("avm_core"),
		vault:         cfg.VaultManager,
		grpcClient:    cfg.GRPCClient,
		scoringParams: cfg.ScoringParams,
		configName:    cfg.ConfigName,
		configVersion: cfg.ConfigVersion,
		cycleCount:    0,
	}

	avm.logger.Info().
		Str("configName", avm.configName).
		Int("configVersion", avm.configVersion).
		Msg("AVM instance created successfully with dependency injection")

	return avm, nil
}

// validateAVMConfig validates the AVM configuration
func validateAVMConfig(cfg Config) error {
	if cfg.GRPCClient == nil {
		return fmt.Errorf("gRPC client cannot be nil")
	}
	if cfg.VaultManager == nil {
		return fmt.Errorf("vault manager cannot be nil")
	}
	if cfg.ScoringParams == nil {
		return fmt.Errorf("scoring parameters cannot be nil")
	}
	if cfg.ConfigName == "" {
		return fmt.Errorf("config name cannot be empty")
	}
	if cfg.ConfigVersion <= 0 {
		return fmt.Errorf("config version must be positive")
	}
	return nil
}

// RunLoop starts the main AVM loop with the specified interval
func (a *AVM) RunLoop(ctx context.Context, interval time.Duration) {
	a.logger.Info().
		Dur("interval", interval).
		Msg("Starting AVM main loop")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run first cycle immediately
	a.cycleCount++
	a.logger.Info().Int("cycle", a.cycleCount).Msg("Initiating AVM cycle")
	a.RunCycle(ctx)
	a.logger.Info().Int("cycle", a.cycleCount).Msg("AVM cycle completed")

	// Continue with ticker
	for {
		select {
		case <-ctx.Done():
			a.logger.Info().Msg("AVM loop stopped due to context cancellation")
			return
		case <-ticker.C:
			a.cycleCount++
			a.logger.Info().Int("cycle", a.cycleCount).Msg("Initiating AVM cycle")
			a.RunCycle(ctx)
			a.logger.Info().Int("cycle", a.cycleCount).Msg("AVM cycle completed")
		}
	}
}

// RunCycle executes a complete AVM rebalancing cycle
func (a *AVM) RunCycle(ctx context.Context) {
	cycleStartTime := time.Now()

	// Generate unique cycle ID for tracing logs across the entire cycle
	cycleID := uuid.New().String()
	cycleLogger := a.logger.With().Str("cycle_id", cycleID).Logger()
	
	cycleLogger.Info().Msg("--- Starting AVM Cycle ---")

	// --- Initialize Cycle Snapshot ---
	cycleSnapshot := types.CycleSnapshot{
		CycleNumber:       a.getCycleNumber(), // Global cycle counter
		Timestamp:         cycleStartTime,
		ScoringParamsID:   a.getScoringParamsID(), // Helper to get current params ID
		TransactionHashes: make([]string, 0),
		ActionReceipts:    make([]types.ActionReceipt, 0),
	}

	cycleLogger.Info().
		Int("cycleNumber", cycleSnapshot.CycleNumber).
		Time("timestamp", cycleStartTime).
		Msg("Cycle snapshot initialized")

	// --- Step 1: Data Fetching ---
	cycleLogger.Info().Msg("Step 1: Fetching live on-chain data...")
	
	// Get supported tokens from vault before fetching pools
	supportedTokens, err := a.vault.GetTradableDenoms()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to get supported tokens from vault.")
		return
	}
	
	cycleLogger.Info().Int("supportedTokenCount", len(supportedTokens)).Msg("Retrieved supported tokens from vault")
	
	pools, err := datafetcher.GetPools(a.grpcClient, supportedTokens)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to fetch pools.")
		return
	}
	poolsDataMap := make(map[types.PoolID]types.Pool)
	for _, p := range pools {
		poolsDataMap[p.ID] = p
	}

	tokenDataMap, err := datafetcher.GetTokens(a.grpcClient)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to fetch token data.")
		return
	}
	cycleLogger.Info().Int("pools", len(poolsDataMap)).Int("tokens", len(tokenDataMap)).Msg("Step 1: Data fetching complete.")

	// --- Step 2: Vault State Assessment & Initial Snapshot Data ---
	cycleLogger.Info().Msg("Step 2: Assessing current vault state...")
	currentPositions, err := a.vault.GetPoolPositions()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to get current positions.")
		return
	}
	liquidUSDC, err := a.vault.GetLiquidUSDC()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to get liquid USDC.")
		return
	}
	totalVaultValue, err := a.vault.GetTotalVaultValue()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to get total vault value.")
		return
	}

	// Populate initial snapshot state
	cycleSnapshot.InitialVaultValueUSD = totalVaultValue
	cycleSnapshot.InitialLiquidUSDC = liquidUSDC
	cycleSnapshot.InitialPositions = a.convertToPositionSnapshots(currentPositions, poolsDataMap, totalVaultValue)

	cycleLogger.Info().Int("positions", len(currentPositions)).Float64("liquidUSDC", liquidUSDC).Float64("totalValue", totalVaultValue).Msg("Step 2: Vault state assessed.")

	// --- Step 3: Analysis & Scoring ---
	cycleLogger.Info().Msg("Step 3: Analyzing and scoring pools...")
	scoredPools, err := analyzer.CalculatePoolScores(pools, *a.scoringParams)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to score pools.")
		return
	}
	selectedPoolIDs, elysPoolID, err := analyzer.SelectTopPools(scoredPools, *a.scoringParams, poolsDataMap)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to select top pools.")
		return
	}

	// Log ELYS pool information
	if elysPoolID != 0 {
		if poolData, exists := poolsDataMap[elysPoolID]; exists {
			cycleLogger.Info().
				Uint64("elysPoolID", uint64(elysPoolID)).
				Str("tokenA", poolData.TokenA.Symbol).
				Str("tokenB", poolData.TokenB.Symbol).
				Msg("ELYS pool identified and included in selection")
		}
	} else {
		cycleLogger.Warn().Msg("No ELYS pool found - forced allocation will not apply")
	}
	
	if len(selectedPoolIDs) == 0 {
		cycleLogger.Info().Msg("No pools selected for investment. No rebalancing needed.")
		// Complete snapshot with no changes
		cycleSnapshot.TargetAllocations = make(map[types.PoolID]float64)
		cycleSnapshot.ActionPlan = types.ActionPlan{
			GoalDescription:       "No rebalancing needed - no pools selected",
			SubActions:            []types.SubAction{}, // Empty since no actions needed
			EstimatedNetUSDChange: 0.0,
		}
		cycleSnapshot.FinalVaultValueUSD = totalVaultValue
		cycleSnapshot.FinalLiquidUSDC = liquidUSDC
		cycleSnapshot.FinalPositions = cycleSnapshot.InitialPositions
		cycleSnapshot.AllocationEfficiencyPercent = 100.0 // Perfect efficiency when no changes needed
		cycleSnapshot.NetReturnUSD = 0.0
		cycleSnapshot.TotalSlippageUSD = 0.0
		cycleSnapshot.TotalGasFeeUSD = 0.0
		a.saveCycleSnapshot(cycleSnapshot)
		a.logEndOfCycleState(cycleStartTime, cycleLogger)
		return
	}
	scoredPoolsMap := make(map[types.PoolID]types.PoolScoreResult)
	for _, sp := range scoredPools {
		scoredPoolsMap[sp.PoolID] = sp
	}
	targetAllocations, err := analyzer.DetermineTargetAllocations(selectedPoolIDs, scoredPoolsMap, *a.scoringParams, elysPoolID)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to determine target allocations.")
		return
	}

	// Capture target allocations in snapshot
	cycleSnapshot.TargetAllocations = targetAllocations

	cycleLogger.Info().Int("selectedPools", len(selectedPoolIDs)).Msg("Step 3: Pool analysis complete.")

	// --- Step 4: Action Planning ---
	cycleLogger.Info().Msg("Step 4: Generating action plan...")
	withdrawalActions, depositActions, err := planner.GenerateActionPlan(
		currentPositions, liquidUSDC, targetAllocations, totalVaultValue,
		poolsDataMap, tokenDataMap, *a.scoringParams, config.NodeRPC,
	)
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Cycle aborted: Failed to generate action plan.")
		return
	}

	// Capture action plan in snapshot
	cycleSnapshot.ActionPlan = types.ActionPlan{
		GoalDescription:       "Rebalancing to target allocations",
		SubActions:            append(withdrawalActions, depositActions...),
		EstimatedNetUSDChange: 0.0, // Would be calculated based on expected value changes
	}

	if len(withdrawalActions) == 0 && len(depositActions) == 0 {
		cycleLogger.Info().Msg("No rebalancing actions required.")
		// Complete snapshot with no changes
		cycleSnapshot.FinalVaultValueUSD = totalVaultValue
		cycleSnapshot.FinalLiquidUSDC = liquidUSDC
		cycleSnapshot.FinalPositions = cycleSnapshot.InitialPositions
		cycleSnapshot.AllocationEfficiencyPercent = a.calculateAllocationEfficiency(cycleSnapshot.InitialPositions, targetAllocations)
		cycleSnapshot.NetReturnUSD = 0.0
		cycleSnapshot.TotalSlippageUSD = 0.0
		cycleSnapshot.TotalGasFeeUSD = 0.0
		a.saveCycleSnapshot(cycleSnapshot)
		a.logEndOfCycleState(cycleStartTime, cycleLogger)
		return
	}
	cycleLogger.Info().Int("withdrawalActions", len(withdrawalActions)).Int("depositActions", len(depositActions)).Msg("Step 4: Action plan generated.")
	planJSON, _ := json.MarshalIndent(struct {
		W []types.SubAction
		D []types.SubAction
	}{withdrawalActions, depositActions}, "", "  ")
	cycleLogger.Info().Str("actionPlan", string(planJSON)).Msg("--- Detailed Action Plan ---")

	// --- Step 5: Action Execution (Two-Phase) ---
	cycleLogger.Info().Msg("Step 5: Executing action plan...")

	// Track total gas fees (slippage will be calculated from final values)
	var totalGasFeeUSD float64

	if len(withdrawalActions) > 0 {
		cycleLogger.Info().Msg("Executing withdrawal/consolidation phase...")

		// Capture vault state before withdrawal
		preWithdrawPositions, preWithdrawUSDC, err := a.captureVaultState()
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Failed to capture vault state before withdrawal")
			// Continue with execution but use current state
			preWithdrawPositions = currentPositions
			preWithdrawUSDC = liquidUSDC
		}

		txResult, err := a.vault.ExecuteActionPlan(withdrawalActions)
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Withdrawal/consolidation transaction failed.")
			// Save snapshot even on failure, marking final state as current state
			a.finalizeFailedSnapshot(&cycleSnapshot, totalVaultValue, liquidUSDC, currentPositions, poolsDataMap)
			a.saveCycleSnapshot(cycleSnapshot)
			a.logEndOfCycleState(cycleStartTime, cycleLogger)
			return
		}
		cycleLogger.Info().Str("txHash", txResult.TxHash).Msg("Withdrawal/consolidation transaction completed successfully.")
		cycleSnapshot.TransactionHashes = append(cycleSnapshot.TransactionHashes, txResult.TxHash)

		// Accumulate gas fees from transaction result
		totalGasFeeUSD += txResult.GasFeeUSD

		// Capture vault state after withdrawal
		postWithdrawPositions, postWithdrawUSDC, err := a.captureVaultState()
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Failed to capture vault state after withdrawal")
			// Use current state as fallback
			postWithdrawPositions = currentPositions
			postWithdrawUSDC = liquidUSDC
		}

		// Generate action receipts for withdrawal actions
		for _, action := range withdrawalActions {
			actualAmountUSD := a.calculateActualAmountUSD(action, preWithdrawPositions, postWithdrawPositions, preWithdrawUSDC, postWithdrawUSDC, poolsDataMap)
			receipt := types.ActionReceipt{
				OriginalSubAction: action,
				Success:           true,
				Message:           "Withdrawal executed successfully",
				Timestamp:         time.Now(),
				ActualAmountUSD:   actualAmountUSD,
			}
			cycleSnapshot.ActionReceipts = append(cycleSnapshot.ActionReceipts, receipt)

			cycleLogger.Info().
				Str("actionType", string(action.Type)).
				Uint64("poolID", uint64(action.PoolIDToWithdraw)).
				Float64("actualAmountUSD", actualAmountUSD).
				Msg("Generated withdrawal action receipt")
		}
	}

	if len(depositActions) > 0 {
		cycleLogger.Info().Msg("Executing deposit phase...")

		// Capture vault state before deposit
		preDepositPositions, preDepositUSDC, err := a.captureVaultState()
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Failed to capture vault state before deposit")
			// Continue with execution but use current state
			preDepositPositions = currentPositions
			preDepositUSDC = liquidUSDC
		}

		txResult, err := a.vault.ExecuteActionPlan(depositActions)
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Deposit transaction failed.")
			// Save snapshot even on failure
			a.finalizeFailedSnapshot(&cycleSnapshot, totalVaultValue, liquidUSDC, currentPositions, poolsDataMap)
			a.saveCycleSnapshot(cycleSnapshot)
			a.logEndOfCycleState(cycleStartTime, cycleLogger)
			return
		}
		cycleLogger.Info().Str("txHash", txResult.TxHash).Msg("Deposit transaction completed successfully.")
		cycleSnapshot.TransactionHashes = append(cycleSnapshot.TransactionHashes, txResult.TxHash)

		// Accumulate gas fees from transaction result
		totalGasFeeUSD += txResult.GasFeeUSD

		// Capture vault state after deposit
		postDepositPositions, postDepositUSDC, err := a.captureVaultState()
		if err != nil {
			cycleLogger.Error().Err(err).Msg("Failed to capture vault state after deposit")
			// Use current state as fallback
			postDepositPositions = currentPositions
			postDepositUSDC = liquidUSDC
		}

		// Generate action receipts for deposit actions
		for _, action := range depositActions {
			actualAmountUSD := a.calculateActualAmountUSD(action, preDepositPositions, postDepositPositions, preDepositUSDC, postDepositUSDC, poolsDataMap)
			receipt := types.ActionReceipt{
				OriginalSubAction: action,
				Success:           true,
				Message:           "Deposit executed successfully",
				Timestamp:         time.Now(),
				ActualAmountUSD:   actualAmountUSD,
			}
			cycleSnapshot.ActionReceipts = append(cycleSnapshot.ActionReceipts, receipt)

			cycleLogger.Info().
				Str("actionType", string(action.Type)).
				Uint64("poolID", uint64(action.PoolIDToDeposit)).
				Float64("actualAmountUSD", actualAmountUSD).
				Msg("Generated deposit action receipt")
		}
	}

	// --- Step 6: Capture Final State & Calculate Performance Metrics ---
	cycleLogger.Info().Msg("Step 6: Capturing final state and calculating performance metrics...")

	finalLiquidUSDC, err := a.vault.GetLiquidUSDC()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final liquid USDC.")
		finalLiquidUSDC = liquidUSDC // Use initial value as fallback
	}

	finalPositions, err := a.vault.GetPoolPositions()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final positions.")
		finalPositions = currentPositions // Use initial positions as fallback
	}

	finalTotalValue, err := a.vault.GetTotalVaultValue()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final total value.")
		finalTotalValue = totalVaultValue // Use initial value as fallback
	}

	// Calculate actual slippage based on final vault values
	// The expected net gain would be minimal (just from potential fee earnings during the rebalance)
	// so slippage is primarily the difference between initial and final value minus gas fees
	netValueChange := finalTotalValue - totalVaultValue
	valueChangeExcludingGas := netValueChange + totalGasFeeUSD // Add back gas fees to see pure trading impact

	// If we lost value beyond just gas fees, that's slippage
	actualSlippageUSD := 0.0
	if valueChangeExcludingGas < 0 {
		actualSlippageUSD = -valueChangeExcludingGas // Convert to positive slippage amount
	}

	// Complete the snapshot
	cycleSnapshot.FinalVaultValueUSD = finalTotalValue
	cycleSnapshot.FinalLiquidUSDC = finalLiquidUSDC
	cycleSnapshot.FinalPositions = a.convertToPositionSnapshots(finalPositions, poolsDataMap, finalTotalValue)
	cycleSnapshot.AllocationEfficiencyPercent = a.calculateAllocationEfficiency(cycleSnapshot.FinalPositions, targetAllocations)
	cycleSnapshot.NetReturnUSD = netValueChange
	cycleSnapshot.TotalSlippageUSD = actualSlippageUSD
	cycleSnapshot.TotalGasFeeUSD = totalGasFeeUSD

	// Save the complete cycle snapshot
	a.saveCycleSnapshot(cycleSnapshot)

	cycleLogger.Info().
		Float64("finalLiquidUSDC", finalLiquidUSDC).
		Int("finalPositionsCount", len(finalPositions)).
		Float64("finalTotalValueUSD", finalTotalValue).
		Float64("netReturnUSD", cycleSnapshot.NetReturnUSD).
		Float64("actualSlippageUSD", actualSlippageUSD).
		Float64("totalGasFeesUSD", totalGasFeeUSD).
		Float64("allocationEfficiency", cycleSnapshot.AllocationEfficiencyPercent).
		Msg("End of Cycle State")

	cycleEndTime := time.Now()
	cycleLogger.Info().Str("cycleDuration", cycleEndTime.Sub(cycleStartTime).String()).Msg("AVM Cycle Duration")
	
	cycleLogger.Info().Msg("--- AVM Cycle Completed Successfully ---")
}


// getCycleNumber increments and returns the persistent cycle counter from database
func (a *AVM) getCycleNumber() int {
	cycleNumber, err := state.IncrementCycleNumber()
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to increment cycle number, using fallback")
		// Fallback to a simple counter if database fails
		return int(time.Now().Unix() % 1000000) // Use timestamp as fallback
	}
	return cycleNumber
}

// getScoringParamsID retrieves the current active scoring parameters ID from database
func (a *AVM) getScoringParamsID() *int64 {
	paramsID, err := state.GetActiveScoringParametersID(a.configName)
	if err != nil {
		a.logger.Error().Err(err).Str("configName", a.configName).Msg("Failed to get active scoring parameters ID")
		return nil
	}
	return paramsID
}

// calculateActualAmountUSD calculates the actual USD amount for an action by comparing vault states
func (a *AVM) calculateActualAmountUSD(
	action types.SubAction,
	initialPositions []types.Position,
	finalPositions []types.Position,
	initialLiquidUSDC, finalLiquidUSDC float64,
	poolsDataMap map[types.PoolID]types.Pool,
) float64 {
	// Validate inputs
	if initialPositions == nil || finalPositions == nil {
		a.logger.Warn().Msg("Cannot calculate actual amount USD: positions data is nil")
		return 0.0
	}

	if math.IsNaN(initialLiquidUSDC) || math.IsNaN(finalLiquidUSDC) ||
		math.IsInf(initialLiquidUSDC, 0) || math.IsInf(finalLiquidUSDC, 0) {
		a.logger.Warn().
			Float64("initialLiquidUSDC", initialLiquidUSDC).
			Float64("finalLiquidUSDC", finalLiquidUSDC).
			Msg("Cannot calculate actual amount USD: invalid USDC values")
		return 0.0
	}

	switch action.Type {
	case types.SubActionWithdrawLP:
		return a.calculateWithdrawActualUSD(action, initialPositions, finalPositions, initialLiquidUSDC, finalLiquidUSDC, poolsDataMap)
	case types.SubActionDepositLP:
		return a.calculateDepositActualUSD(action, initialPositions, finalPositions, initialLiquidUSDC, finalLiquidUSDC, poolsDataMap)
	case types.SubActionSwap:
		return a.calculateSwapActualUSD(action, initialLiquidUSDC, finalLiquidUSDC)
	default:
		a.logger.Warn().Str("actionType", string(action.Type)).Msg("Unknown action type for actual amount USD calculation")
		return 0.0
	}
}

// calculateWithdrawActualUSD calculates the actual USD value withdrawn from a pool
func (a *AVM) calculateWithdrawActualUSD(
	action types.SubAction,
	initialPositions []types.Position,
	finalPositions []types.Position,
	initialLiquidUSDC, finalLiquidUSDC float64,
	poolsDataMap map[types.PoolID]types.Pool,
) float64 {
	if action.Type != types.SubActionWithdrawLP {
		return 0.0
	}

	// Find the pool position in initial and final states
	var initialPos, finalPos *types.Position
	for i := range initialPositions {
		if initialPositions[i].PoolID == action.PoolIDToWithdraw {
			initialPos = &initialPositions[i]
			break
		}
	}
	for i := range finalPositions {
		if finalPositions[i].PoolID == action.PoolIDToWithdraw {
			finalPos = &finalPositions[i]
			break
		}
	}

	// Calculate value change in the pool position
	var poolValueReduction float64
	if initialPos != nil && finalPos != nil {
		// Pool position still exists but reduced
		poolValueReduction = initialPos.EstimatedValue - finalPos.EstimatedValue
	} else if initialPos != nil && finalPos == nil {
		// Pool position completely exited
		poolValueReduction = initialPos.EstimatedValue
	}

	// The USDC increase should approximately equal the pool value reduction (minus slippage/fees)
	usdcIncrease := finalLiquidUSDC - initialLiquidUSDC

	a.logger.Debug().
		Uint64("poolID", uint64(action.PoolIDToWithdraw)).
		Float64("poolValueReduction", poolValueReduction).
		Float64("usdcIncrease", usdcIncrease).
		Float64("initialLiquidUSDC", initialLiquidUSDC).
		Float64("finalLiquidUSDC", finalLiquidUSDC).
		Msg("Calculating withdrawal actual amount USD")

	// Use the pool value reduction as the primary measure, but validate with USDC change
	if poolValueReduction > 0 {
		// Validate the calculated value is reasonable
		if math.IsNaN(poolValueReduction) || math.IsInf(poolValueReduction, 0) {
			a.logger.Warn().Float64("poolValueReduction", poolValueReduction).Msg("Invalid pool value reduction calculated")
			return 0.0
		}
		return poolValueReduction
	} else if usdcIncrease > 0 {
		// Fallback to USDC increase if pool value calculation fails
		if math.IsNaN(usdcIncrease) || math.IsInf(usdcIncrease, 0) {
			a.logger.Warn().Float64("usdcIncrease", usdcIncrease).Msg("Invalid USDC increase calculated")
			return 0.0
		}
		return usdcIncrease
	}

	return 0.0
}

// calculateDepositActualUSD calculates the actual USD value deposited into a pool
func (a *AVM) calculateDepositActualUSD(
	action types.SubAction,
	initialPositions []types.Position,
	finalPositions []types.Position,
	initialLiquidUSDC, finalLiquidUSDC float64,
	poolsDataMap map[types.PoolID]types.Pool,
) float64 {
	if action.Type != types.SubActionDepositLP {
		return 0.0
	}

	// Find the pool position in initial and final states
	var initialPos, finalPos *types.Position
	for i := range initialPositions {
		if initialPositions[i].PoolID == action.PoolIDToDeposit {
			initialPos = &initialPositions[i]
			break
		}
	}
	for i := range finalPositions {
		if finalPositions[i].PoolID == action.PoolIDToDeposit {
			finalPos = &finalPositions[i]
			break
		}
	}

	// Calculate value increase in the pool position
	var poolValueIncrease float64
	if initialPos != nil && finalPos != nil {
		// Pool position existed and increased
		poolValueIncrease = finalPos.EstimatedValue - initialPos.EstimatedValue
	} else if initialPos == nil && finalPos != nil {
		// New pool position created
		poolValueIncrease = finalPos.EstimatedValue
	}

	// The USDC decrease should approximately equal the pool value increase (plus slippage/fees)
	usdcDecrease := initialLiquidUSDC - finalLiquidUSDC

	a.logger.Debug().
		Uint64("poolID", uint64(action.PoolIDToDeposit)).
		Float64("poolValueIncrease", poolValueIncrease).
		Float64("usdcDecrease", usdcDecrease).
		Float64("initialLiquidUSDC", initialLiquidUSDC).
		Float64("finalLiquidUSDC", finalLiquidUSDC).
		Msg("Calculating deposit actual amount USD")

	// Use the pool value increase as the primary measure, but validate with USDC change
	if poolValueIncrease > 0 {
		// Validate the calculated value is reasonable
		if math.IsNaN(poolValueIncrease) || math.IsInf(poolValueIncrease, 0) {
			a.logger.Warn().Float64("poolValueIncrease", poolValueIncrease).Msg("Invalid pool value increase calculated")
			return 0.0
		}
		return poolValueIncrease
	} else if usdcDecrease > 0 {
		// Fallback to USDC decrease if pool value calculation fails
		if math.IsNaN(usdcDecrease) || math.IsInf(usdcDecrease, 0) {
			a.logger.Warn().Float64("usdcDecrease", usdcDecrease).Msg("Invalid USDC decrease calculated")
			return 0.0
		}
		return usdcDecrease
	}

	return 0.0
}

// calculateSwapActualUSD calculates the actual USD value of a swap transaction
func (a *AVM) calculateSwapActualUSD(
	action types.SubAction,
	initialLiquidUSDC, finalLiquidUSDC float64,
) float64 {
	if action.Type != types.SubActionSwap {
		return 0.0
	}

	// For swaps, we use the absolute change in USDC as the transaction amount
	// This represents the value that was swapped (either from or to USDC)
	usdcChange := finalLiquidUSDC - initialLiquidUSDC

	a.logger.Debug().
		Str("tokenIn", action.TokenIn.String()).
		Str("tokenOutDenom", action.TokenOutDenom).
		Float64("usdcChange", usdcChange).
		Float64("initialLiquidUSDC", initialLiquidUSDC).
		Float64("finalLiquidUSDC", finalLiquidUSDC).
		Msg("Calculating swap actual amount USD")

	if usdcChange < 0 {
		// USDC decreased (swapped USDC for another token)
		actualAmount := -usdcChange
		if math.IsNaN(actualAmount) || math.IsInf(actualAmount, 0) || actualAmount < 0 {
			a.logger.Warn().Float64("actualAmount", actualAmount).Msg("Invalid swap amount calculated")
			return 0.0
		}
		return actualAmount
	} else {
		// USDC increased (swapped another token for USDC)
		if math.IsNaN(usdcChange) || math.IsInf(usdcChange, 0) || usdcChange < 0 {
			a.logger.Warn().Float64("usdcChange", usdcChange).Msg("Invalid swap amount calculated")
			return 0.0
		}
		return usdcChange
	}
}

// captureVaultState captures the current vault state for comparison
func (a *AVM) captureVaultState() ([]types.Position, float64, error) {
	positions, err := a.vault.GetPoolPositions()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get pool positions: %w", err)
	}

	liquidUSDC, err := a.vault.GetLiquidUSDC()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get liquid USDC: %w", err)
	}

	return positions, liquidUSDC, nil
}

// convertToPositionSnapshots converts positions to snapshots with additional metadata
func (a *AVM) convertToPositionSnapshots(positions []types.Position, poolsDataMap map[types.PoolID]types.Pool, totalVaultValue float64) []types.PositionSnapshot {
	snapshots := make([]types.PositionSnapshot, len(positions))
	for i, pos := range positions {
		pool, exists := poolsDataMap[pos.PoolID]
		var tokenA, tokenB string
		var poolTVL, poolScore float64

		if exists {
			// Get token symbols from pool tokens
			tokenA = pool.TokenA.Symbol
			tokenB = pool.TokenB.Symbol
			poolTVL = pool.TvlUSD
			// Use the pool's score result if available
			poolScore = pool.Score.Score
		}

		allocationPercent := 0.0
		if totalVaultValue > 0 {
			allocationPercent = (pos.EstimatedValue / totalVaultValue) * 100.0
		}

		snapshots[i] = types.PositionSnapshot{
			PoolID:            pos.PoolID,
			LPShares:          pos.LPShares.String(),
			EstimatedValueUSD: pos.EstimatedValue,
			AllocationPercent: allocationPercent,
			AgeDays:           pos.AgeDays,
			PoolTokenA:        tokenA,
			PoolTokenB:        tokenB,
			PoolTVL:           poolTVL,
			PoolScore:         poolScore,
		}
	}
	return snapshots
}

// calculateAllocationEfficiency calculates how well the final allocations match targets
func (a *AVM) calculateAllocationEfficiency(finalPositions []types.PositionSnapshot, targetAllocations map[types.PoolID]float64) float64 {
	if len(targetAllocations) == 0 {
		return 100.0 // Perfect efficiency if no targets
	}

	// Calculate actual allocations from final positions
	actualAllocations := make(map[types.PoolID]float64)
	for _, pos := range finalPositions {
		actualAllocations[pos.PoolID] = pos.AllocationPercent / 100.0 // Convert to fraction
	}

	// Calculate efficiency as 100% minus the sum of absolute differences
	var totalDifference float64
	for poolID, targetPercent := range targetAllocations {
		actualPercent := actualAllocations[poolID]
		difference := targetPercent - actualPercent
		if difference < 0 {
			difference = -difference // Absolute value
		}
		totalDifference += difference
	}

	// Efficiency is 100% minus the total percentage point difference
	efficiency := 100.0 - (totalDifference * 100.0)
	if efficiency < 0 {
		efficiency = 0
	}
	return efficiency
}

// finalizeFailedSnapshot marks final state as same as initial state since transaction failed
func (a *AVM) finalizeFailedSnapshot(snapshot *types.CycleSnapshot, totalVaultValue, liquidUSDC float64, positions []types.Position, poolsDataMap map[types.PoolID]types.Pool) {
	snapshot.FinalVaultValueUSD = totalVaultValue
	snapshot.FinalLiquidUSDC = liquidUSDC
	snapshot.FinalPositions = a.convertToPositionSnapshots(positions, poolsDataMap, totalVaultValue)
	snapshot.AllocationEfficiencyPercent = 0.0 // Failed execution
	snapshot.NetReturnUSD = 0.0
	snapshot.TotalSlippageUSD = 0.0
	snapshot.TotalGasFeeUSD = 0.0
}

// saveCycleSnapshot saves the cycle snapshot to database
func (a *AVM) saveCycleSnapshot(snapshot types.CycleSnapshot) {
	snapshotID, err := state.SaveCycleSnapshot(snapshot)
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to save cycle snapshot to database")
		return
	}
	a.logger.Info().Int64("snapshot_id", snapshotID).Msg("Cycle snapshot saved successfully")
}

// logEndOfCycleState fetches and logs the final state of the vault for the cycle
func (a *AVM) logEndOfCycleState(cycleStartTime time.Time, cycleLogger zerolog.Logger) {
	cycleLogger.Info().Msg("Step 6: Logging final vault state...")

	finalLiquidUSDC, err := a.vault.GetLiquidUSDC()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final liquid USDC for logging.")
		finalLiquidUSDC = -1 // Indicate error
	}

	finalPositions, err := a.vault.GetPoolPositions()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final positions for logging.")
	}

	finalTotalValue, err := a.vault.GetTotalVaultValue()
	if err != nil {
		cycleLogger.Error().Err(err).Msg("Failed to get final total value for logging.")
		finalTotalValue = -1 // Indicate error
	}

	cycleLogger.Info().
		Float64("finalLiquidUSDC", finalLiquidUSDC).
		Int("finalPositionsCount", len(finalPositions)).
		Float64("finalTotalValueUSD", finalTotalValue).
		Msg("End of Cycle State")

	cycleEndTime := time.Now()
	cycleLogger.Info().Str("cycleDuration", cycleEndTime.Sub(cycleStartTime).String()).Msg("AVM Cycle Duration")
} 
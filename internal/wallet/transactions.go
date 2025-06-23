package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types"
	vaulttypes "github.com/elys-network/elys/v6/x/vaults/types"
)

// Error definitions for zero-tolerance error handling
var (
	ErrInvalidSubAction       = errors.New("sub action contains invalid data")
	ErrInvalidTokenAmount     = errors.New("token amount is invalid")
	ErrInvalidDenom           = errors.New("token denomination is invalid")
	ErrInvalidSlippage        = errors.New("slippage parameters are invalid")
	ErrInvalidPoolID          = errors.New("pool ID is invalid")
	ErrInvalidVaultID         = errors.New("vault ID is invalid")
	ErrInvalidShares          = errors.New("share amount is invalid")
	ErrMathematicalError      = errors.New("mathematical calculation error")
	ErrAccountRetrievalFailed = errors.New("account retrieval failed")
	ErrSigningFailed          = errors.New("transaction signing failed")
	ErrBroadcastFailed        = errors.New("transaction broadcast failed")
)

var txLogger = logger.GetForComponent("transaction_builder")

// TransactionBuilder handles transaction building with gas simulation
type TransactionBuilder struct {
	signingClient *SigningClient
}

// NewTransactionBuilder creates a new transaction builder
func NewTransactionBuilder(signingClient *SigningClient) *TransactionBuilder {
	return &TransactionBuilder{
		signingClient: signingClient,
	}
}

// ProcessSubActions processes a list of SubActions and executes them as a transaction
func (tb *TransactionBuilder) ProcessSubActions(subActions []types.SubAction, vaultId uint64) (*sdk.TxResponse, error) {
	txLogger.Info().
		Int("actionCount", len(subActions)).
		Uint64("vaultId", vaultId).
		Msg("ProcessSubActions: Starting transaction processing")

	// Validate inputs
	if len(subActions) == 0 {
		return nil, errors.New("no sub-actions provided")
	}

	// Convert SubActions to SDK messages
	msgs, err := tb.SubActionsToMessages(subActions, vaultId)
	if err != nil {
		txLogger.Error().Err(err).Msg("ProcessSubActions: Failed to convert sub-actions to messages")
		return nil, fmt.Errorf("failed to convert sub-actions to messages: %w", err)
	}

	txLogger.Info().
		Int("messageCount", len(msgs)).
		Msg("ProcessSubActions: Sub-actions converted to SDK messages")

	// Create context for transaction
	ctx := context.Background()

	// Estimate gas using simulation
	txLogger.Info().Msg("ProcessSubActions: Estimating gas using simulation...")
	estimatedGas, err := tb.simulateGas(ctx, msgs...)
	if err != nil {
		txLogger.Warn().Err(err).Msg("ProcessSubActions: Gas simulation failed, using default gas")
		estimatedGas = config.DefaultGasLimit
	}

	txLogger.Info().
		Uint64("estimatedGas", estimatedGas).
		Msg("ProcessSubActions: Gas estimation completed")

	// Update the signing client's factory with estimated gas
	tb.signingClient.txFactory = tb.signingClient.txFactory.WithGas(estimatedGas)

	// Sign and broadcast the transaction
	txLogger.Info().Msg("ProcessSubActions: Signing and broadcasting transaction...")
	txResponse, err := tb.signingClient.SignAndBroadcastTx(ctx, msgs...)
	if err != nil {
		txLogger.Error().Err(err).Msg("ProcessSubActions: Transaction failed")
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	txLogger.Info().
		Str("txHash", txResponse.TxHash).
		Uint32("code", txResponse.Code).
		Msg("ProcessSubActions: Transaction completed successfully")

	return txResponse, nil
}

// simulateGas simulates a transaction to estimate gas usage
func (tb *TransactionBuilder) simulateGas(ctx context.Context, msgs ...sdk.Msg) (uint64, error) {
	txLogger.Info().
		Int("messageCount", len(msgs)).
		Msg("simulateGas: Starting gas simulation")

	// Validate inputs
	if len(msgs) == 0 {
		return 0, errors.New("no messages provided for gas simulation")
	}

	// Get account info for simulation
	account, err := tb.signingClient.clientCtx.AccountRetriever.GetAccount(tb.signingClient.clientCtx, tb.signingClient.fromAddress)
	if err != nil {
		return 0, fmt.Errorf("failed to get account info for simulation: %w", err)
	}

	// Create simulation factory
	simulationFactory := tb.signingClient.txFactory.
		WithAccountNumber(account.GetAccountNumber()).
		WithSequence(account.GetSequence()).
		WithGas(0). // Set gas to 0 for simulation
		WithGasAdjustment(config.GasAdjustment).
		WithGasPrices(config.GasPriceAmount + config.GasPriceDenom)

	// Build simulation transaction
	txBytes, err := simulationFactory.BuildSimTx(msgs...)
	if err != nil {
		return 0, fmt.Errorf("failed to build simulation transaction: %w", err)
	}

	// Create tx service client
	txSvcClient := txtypes.NewServiceClient(tb.signingClient.grpcConn)

	// Execute simulation
	simRequest := &txtypes.SimulateRequest{
		TxBytes: txBytes,
	}

	simRes, err := txSvcClient.Simulate(ctx, simRequest)
	if err != nil {
		return 0, fmt.Errorf("gas simulation failed: %w", err)
	}

	if simRes == nil || simRes.GasInfo == nil {
		return 0, errors.New("simulation response or gas info is nil")
	}

	// Calculate adjusted gas
	simulatedGas := simRes.GasInfo.GasUsed
	if simulatedGas == 0 {
		return 0, errors.New("simulated gas usage is zero")
	}

	gasAdjustment := simulationFactory.GasAdjustment()
	adjustedGas := uint64(gasAdjustment * float64(simulatedGas))
	
	// Add safety buffer
	finalGas := adjustedGas + 10000

	txLogger.Info().
		Uint64("simulatedGas", simulatedGas).
		Float64("gasAdjustment", gasAdjustment).
		Uint64("adjustedGas", adjustedGas).
		Uint64("finalGas", finalGas).
		Msg("simulateGas: Gas simulation completed successfully")

	return finalGas, nil
}

// SubActionsToMessages converts SubActions to messages with comprehensive validation
func (tb *TransactionBuilder) SubActionsToMessages(subActions []types.SubAction, vaultID uint64) ([]sdk.Msg, error) {
	txLogger.Info().
		Int("actionCount", len(subActions)).
		Uint64("vaultID", vaultID).
		Msg("SubActionsToMessages: Starting conversion")

	// Validate inputs
	if len(subActions) == 0 {
		txLogger.Error().Msg("SubActionsToMessages: Sub actions cannot be empty")
		return nil, errors.New("sub actions cannot be empty")
	}
	if vaultID == 0 {
		txLogger.Error().Msg("SubActionsToMessages: Vault ID cannot be zero")
		return nil, errors.Join(ErrInvalidVaultID, errors.New("vault ID cannot be zero"))
	}

	txLogger.Info().Msg("SubActionsToMessages: Input validation passed")

	var messages []sdk.Msg

	for i, subAction := range subActions {
		txLogger.Debug().
			Int("actionIndex", i).
			Str("actionType", string(subAction.Type)).
			Msg("SubActionsToMessages: Converting SubAction to message")

		// Validate the sub action before processing
		if err := validateSubAction(subAction, i); err != nil {
			txLogger.Error().Err(err).Int("actionIndex", i).Msg("SubActionsToMessages: SubAction validation failed")
			return nil, errors.Join(ErrInvalidSubAction, fmt.Errorf("action %d validation failed: %w", i, err))
		}

		txLogger.Debug().
			Int("actionIndex", i).
			Msg("SubActionsToMessages: SubAction validation passed")

		var msg sdk.Msg
		var err error

		switch subAction.Type {
		case types.SubActionSwap:
			txLogger.Debug().Int("actionIndex", i).Msg("SubActionsToMessages: Creating swap message")
			msg, err = tb.createSwapMessage(subAction, vaultID)
			if err != nil {
				txLogger.Error().Err(err).Int("actionIndex", i).Msg("SubActionsToMessages: Failed to create swap message")
				return nil, fmt.Errorf("failed to create swap message for action %d: %w", i, err)
			}

		case types.SubActionDepositLP:
			txLogger.Debug().Int("actionIndex", i).Msg("SubActionsToMessages: Creating deposit LP message")
			msg, err = tb.createDepositLPMessage(subAction, vaultID)
			if err != nil {
				txLogger.Error().Err(err).Int("actionIndex", i).Msg("SubActionsToMessages: Failed to create deposit LP message")
				return nil, fmt.Errorf("failed to create deposit LP message for action %d: %w", i, err)
			}

		case types.SubActionWithdrawLP:
			txLogger.Debug().Int("actionIndex", i).Msg("SubActionsToMessages: Creating withdraw LP message")
			msg, err = tb.createWithdrawLPMessage(subAction, vaultID)
			if err != nil {
				txLogger.Error().Err(err).Int("actionIndex", i).Msg("SubActionsToMessages: Failed to create withdraw LP message")
				return nil, fmt.Errorf("failed to create withdraw LP message for action %d: %w", i, err)
			}

		default:
			txLogger.Error().
				Int("actionIndex", i).
				Str("actionType", string(subAction.Type)).
				Msg("SubActionsToMessages: Unknown SubAction type")
			return nil, fmt.Errorf("unknown SubAction type: %s", subAction.Type)
		}

		txLogger.Debug().
			Int("actionIndex", i).
			Str("actionType", string(subAction.Type)).
			Msg("SubActionsToMessages: Message created successfully")

		// Validate the created message
		if err := validateMessage(msg); err != nil {
			txLogger.Error().Err(err).Int("actionIndex", i).Msg("SubActionsToMessages: Created message validation failed")
			return nil, fmt.Errorf("created message validation failed for action %d: %w", i, err)
		}

		txLogger.Debug().
			Int("actionIndex", i).
			Msg("SubActionsToMessages: Message validation passed")

		messages = append(messages, msg)
	}

	if len(messages) == 0 {
		txLogger.Error().Msg("SubActionsToMessages: No valid messages were created")
		return nil, errors.New("no valid messages were created")
	}

	txLogger.Info().
		Int("messageCount", len(messages)).
		Msg("SubActionsToMessages: Successfully converted SubActions to messages")

	return messages, nil
}

// validateSubAction performs comprehensive validation of a SubAction
func validateSubAction(subAction types.SubAction, index int) error {
	// Validate action type
	switch subAction.Type {
	case types.SubActionSwap, types.SubActionDepositLP, types.SubActionWithdrawLP:
		// Valid types
	case "":
		return fmt.Errorf("action %d type cannot be empty", index)
	default:
		return fmt.Errorf("action %d has unknown type: %s", index, subAction.Type)
	}

	// Type-specific validation
	switch subAction.Type {
	case types.SubActionSwap:
		return validateSwapAction(subAction, index)
	case types.SubActionDepositLP:
		return validateDepositAction(subAction, index)
	case types.SubActionWithdrawLP:
		return validateWithdrawAction(subAction, index)
	}

	return nil
}

// validateSwapAction validates swap-specific fields
func validateSwapAction(subAction types.SubAction, index int) error {
	// Validate token input
	if subAction.TokenIn.Amount.IsNil() {
		return fmt.Errorf("swap action %d: token in amount is nil", index)
	}
	if subAction.TokenIn.Amount.IsZero() {
		return fmt.Errorf("swap action %d: token in amount cannot be zero", index)
	}
	if subAction.TokenIn.Amount.IsNegative() {
		return fmt.Errorf("swap action %d: token in amount cannot be negative", index)
	}
	if subAction.TokenIn.Denom == "" {
		return fmt.Errorf("swap action %d: token in denom cannot be empty", index)
	}
	if subAction.TokenOutDenom == "" {
		return fmt.Errorf("swap action %d: token out denom cannot be empty", index)
	}
	if subAction.TokenIn.Denom == subAction.TokenOutDenom {
		return fmt.Errorf("swap action %d: input and output denoms cannot be the same", index)
	}

	// Validate expected output if provided
	if !subAction.ExpectedTokenOut.IsNil() && subAction.ExpectedTokenOut.IsNegative() {
		return fmt.Errorf("swap action %d: expected token out cannot be negative", index)
	}

	// Validate slippage
	if err := validateSlippageParameters(subAction.SlippageTolerancePct, subAction.ExpectedSlippage, index); err != nil {
		return err
	}

	return nil
}

// validateDepositAction validates deposit-specific fields
func validateDepositAction(subAction types.SubAction, index int) error {
	// Validate pool ID
	if subAction.PoolIDToDeposit == 0 {
		return fmt.Errorf("deposit action %d: pool ID cannot be zero", index)
	}

	// Validate amounts to deposit
	if len(subAction.AmountsToDeposit) == 0 {
		return fmt.Errorf("deposit action %d: amounts to deposit cannot be empty", index)
	}

	for i, coin := range subAction.AmountsToDeposit {
		if coin.Amount.IsNil() {
			return fmt.Errorf("deposit action %d: coin %d amount is nil", index, i)
		}
		if coin.Amount.IsZero() {
			return fmt.Errorf("deposit action %d: coin %d amount cannot be zero", index, i)
		}
		if coin.Amount.IsNegative() {
			return fmt.Errorf("deposit action %d: coin %d amount cannot be negative", index, i)
		}
		if coin.Denom == "" {
			return fmt.Errorf("deposit action %d: coin %d denom cannot be empty", index, i)
		}
	}

	// Validate expected shares if provided
	if !subAction.ExpectedSharesOut.IsNil() && subAction.ExpectedSharesOut.IsNegative() {
		return fmt.Errorf("deposit action %d: expected shares out cannot be negative", index)
	}

	// Validate slippage
	if err := validateSlippageParameters(subAction.SlippageTolerancePct, subAction.ExpectedSlippage, index); err != nil {
		return err
	}

	return nil
}

// validateWithdrawAction validates withdrawal-specific fields
func validateWithdrawAction(subAction types.SubAction, index int) error {
	// Validate pool ID
	if subAction.PoolIDToWithdraw == 0 {
		return fmt.Errorf("withdraw action %d: pool ID cannot be zero", index)
	}

	// Validate LP shares to withdraw
	if subAction.LPSharesToWithdraw.IsNil() {
		return fmt.Errorf("withdraw action %d: LP shares to withdraw is nil", index)
	}
	if subAction.LPSharesToWithdraw.IsZero() {
		return fmt.Errorf("withdraw action %d: LP shares to withdraw cannot be zero", index)
	}
	if subAction.LPSharesToWithdraw.IsNegative() {
		return fmt.Errorf("withdraw action %d: LP shares to withdraw cannot be negative", index)
	}

	// Target denom on exit can be empty (means proportional exit)
	// but if provided, must be valid
	if subAction.TargetDenomOnExit != "" {
		// Additional validation for target denom could be added here
	}

	// Validate slippage protection parameters if provided
	if len(subAction.ExpectedAmountsOut) > 0 {
		// Validate slippage tolerance
		if err := validateSlippageParameters(subAction.SlippageTolerancePct, subAction.ExpectedSlippage, index); err != nil {
			return fmt.Errorf("withdraw action %d slippage validation failed: %w", index, err)
		}
		
		// Validate expected amounts out
		for i, expectedCoin := range subAction.ExpectedAmountsOut {
			if err := validateCoin(expectedCoin, fmt.Sprintf("withdraw action %d expected amount %d", index, i)); err != nil {
				return fmt.Errorf("withdraw action %d has invalid expected amount %d: %w", index, i, err)
			}
		}
	}

	return nil
}

// validateSlippageParameters validates slippage-related parameters
func validateSlippageParameters(slippageTolerance, expectedSlippage float64, index int) error {
	// Validate slippage tolerance
	if math.IsNaN(slippageTolerance) || math.IsInf(slippageTolerance, 0) {
		return fmt.Errorf("action %d: slippage tolerance is not finite", index)
	}
	if slippageTolerance < 0 {
		return fmt.Errorf("action %d: slippage tolerance cannot be negative", index)
	}
	if slippageTolerance > 1 {
		return fmt.Errorf("action %d: slippage tolerance cannot exceed 100%%", index)
	}

	// Validate expected slippage if provided
	if expectedSlippage != 0 {
		if math.IsNaN(expectedSlippage) || math.IsInf(expectedSlippage, 0) {
			return fmt.Errorf("action %d: expected slippage is not finite", index)
		}
		if expectedSlippage < 0 {
			return fmt.Errorf("action %d: expected slippage cannot be negative", index)
		}
		if expectedSlippage > 1 {
			return fmt.Errorf("action %d: expected slippage cannot exceed 100%%", index)
		}
	}

	return nil
}

// validateMessage validates the created SDK message
func validateMessage(msg sdk.Msg) error {
	if msg == nil {
		return errors.New("message cannot be nil")
	}

	// Validate the message using SDK's ValidateBasic if available
	if validator, ok := msg.(interface{ ValidateBasic() error }); ok {
		if err := validator.ValidateBasic(); err != nil {
			return fmt.Errorf("message validation failed: %w", err)
		}
	}

	return nil
}

// createSwapMessage creates a swap message with comprehensive validation
func (tb *TransactionBuilder) createSwapMessage(subAction types.SubAction, vaultID uint64) (sdk.Msg, error) {
	// Calculate minimum amount with proper error handling
	minAmount, err := calculateMinimumSwapAmount(subAction)
	if err != nil {
		return nil, errors.Join(ErrMathematicalError, fmt.Errorf("failed to calculate minimum swap amount: %w", err))
	}

	// Validate calculated minimum amount
	if minAmount.IsNil() || minAmount.IsNegative() {
		return nil, errors.Join(ErrInvalidTokenAmount, errors.New("calculated minimum amount is invalid"))
	}

	// Create and validate the message
	msg := &vaulttypes.MsgPerformActionSwapByDenom{
		Creator:  tb.signingClient.GetAddressString(),
		VaultId:  vaultID,
		Amount:   subAction.TokenIn,
		DenomIn:  subAction.TokenIn.Denom,
		DenomOut: subAction.TokenOutDenom,
		MinAmount: sdk.Coin{
			Denom:  subAction.TokenOutDenom,
			Amount: minAmount,
		},
		MaxAmount: sdk.Coin{
			Denom:  subAction.TokenOutDenom,
			Amount: sdkmath.NewInt(1000000000000000000), // Large max amount
		},
	}

	// Validate message fields
	if err := validateSwapMessage(msg); err != nil {
		return nil, err
	}

	txLogger.Debug().
		Str("tokenIn", fmt.Sprintf("%s%s", subAction.TokenIn.Amount.String(), subAction.TokenIn.Denom)).
		Str("tokenOut", subAction.TokenOutDenom).
		Str("expectedOut", subAction.ExpectedTokenOut.String()).
		Str("minAmount", minAmount.String()).
		Float64("slippageTolerance", subAction.SlippageTolerancePct).
		Uint64("vaultId", vaultID).
		Msg("Created vault swap message with slippage protection")

	return msg, nil
}

// calculateMinimumSwapAmount calculates the minimum output amount for a swap
func calculateMinimumSwapAmount(subAction types.SubAction) (sdkmath.Int, error) {
	// Default minimum amount
	minAmount := sdkmath.NewInt(1)

	// If we have expected output and slippage tolerance, calculate minimum
	if !subAction.ExpectedTokenOut.IsZero() && subAction.SlippageTolerancePct > 0 {
		// Validate inputs for calculation
		if subAction.ExpectedTokenOut.IsNegative() {
			return sdkmath.ZeroInt(), errors.New("expected token out cannot be negative")
		}
		if subAction.SlippageTolerancePct >= 1.0 {
			return sdkmath.ZeroInt(), errors.New("slippage tolerance must be less than 100%")
		}

		// Calculate tolerance factor: (1 - slippageTolerance)
		toleranceFactor := 1.0 - subAction.SlippageTolerancePct
		if toleranceFactor <= 0 {
			return sdkmath.ZeroInt(), errors.New("tolerance factor must be positive")
		}

		// Use decimal arithmetic for precision
		expectedDec := sdkmath.LegacyNewDecFromInt(subAction.ExpectedTokenOut)
		toleranceFactorDec, err := sdkmath.LegacyNewDecFromStr(fmt.Sprintf("%.18f", toleranceFactor))
		if err != nil {
			return sdkmath.ZeroInt(), fmt.Errorf("failed to create tolerance factor decimal: %w", err)
		}

		minAmountDec := expectedDec.Mul(toleranceFactorDec)
		calculatedMinAmount := minAmountDec.TruncateInt()

		// Ensure minimum is at least 1
		if calculatedMinAmount.IsZero() {
			minAmount = sdkmath.NewInt(1)
		} else {
			minAmount = calculatedMinAmount
		}

		// Validate the result
		if minAmount.IsNegative() {
			return sdkmath.ZeroInt(), errors.New("calculated minimum amount is negative")
		}
	}

	return minAmount, nil
}

// validateSwapMessage validates a swap message
func validateSwapMessage(msg *vaulttypes.MsgPerformActionSwapByDenom) error {
	if msg.Creator == "" {
		return errors.New("swap message creator cannot be empty")
	}
	if msg.VaultId == 0 {
		return errors.New("swap message vault ID cannot be zero")
	}
	if msg.Amount.Amount.IsNil() || msg.Amount.Amount.IsZero() || msg.Amount.Amount.IsNegative() {
		return errors.New("swap message amount is invalid")
	}
	if msg.Amount.Denom == "" {
		return errors.New("swap message input denom cannot be empty")
	}
	if msg.DenomOut == "" {
		return errors.New("swap message output denom cannot be empty")
	}
	if msg.MinAmount.Amount.IsNil() || msg.MinAmount.Amount.IsNegative() {
		return errors.New("swap message minimum amount is invalid")
	}
	if msg.MaxAmount.Amount.IsNil() || msg.MaxAmount.Amount.IsNegative() {
		return errors.New("swap message maximum amount is invalid")
	}
	return nil
}

// createDepositLPMessage creates a deposit LP message with comprehensive validation
func (tb *TransactionBuilder) createDepositLPMessage(subAction types.SubAction, vaultID uint64) (sdk.Msg, error) {
	// Calculate minimum shares with proper error handling
	minShares, err := calculateMinimumShares(subAction)
	if err != nil {
		return nil, errors.Join(ErrMathematicalError, fmt.Errorf("failed to calculate minimum shares: %w", err))
	}

	// Validate calculated minimum shares
	if minShares.IsNil() || minShares.IsNegative() {
		return nil, errors.Join(ErrInvalidShares, errors.New("calculated minimum shares is invalid"))
	}

	// Validate and copy amounts to deposit
	var maxAmountsIn []sdk.Coin
	for i, coin := range subAction.AmountsToDeposit {
		if err := validateCoin(coin, fmt.Sprintf("deposit amount %d", i)); err != nil {
			return nil, err
		}
		maxAmountsIn = append(maxAmountsIn, coin)
	}

	// Create and validate the message
	msg := &vaulttypes.MsgPerformActionJoinPool{
		Creator:        tb.signingClient.GetAddressString(),
		VaultId:        vaultID,
		PoolId:         uint64(subAction.PoolIDToDeposit),
		MaxAmountsIn:   maxAmountsIn,
		ShareAmountOut: minShares,
	}

	// Validate message fields
	if err := validateDepositMessage(msg); err != nil {
		return nil, err
	}

	txLogger.Debug().
		Uint64("poolId", uint64(subAction.PoolIDToDeposit)).
		Str("expectedShares", subAction.ExpectedSharesOut.String()).
		Str("minShares", minShares.String()).
		Float64("slippageTolerance", subAction.SlippageTolerancePct).
		Int("amountCount", len(subAction.AmountsToDeposit)).
		Msg("Created vault join pool message with slippage protection")

	return msg, nil
}

// calculateMinimumShares calculates the minimum shares for a deposit
func calculateMinimumShares(subAction types.SubAction) (sdkmath.Int, error) {
	// Default minimum shares
	minShares := sdkmath.NewInt(1)

	// If we have expected shares and slippage tolerance, calculate minimum
	if !subAction.ExpectedSharesOut.IsZero() && subAction.SlippageTolerancePct > 0 {
		// Validate inputs for calculation
		if subAction.ExpectedSharesOut.IsNegative() {
			return sdkmath.ZeroInt(), errors.New("expected shares out cannot be negative")
		}
		if subAction.SlippageTolerancePct >= 1.0 {
			return sdkmath.ZeroInt(), errors.New("slippage tolerance must be less than 100%")
		}

		// Calculate tolerance factor: (1 - slippageTolerance)
		toleranceFactor := 1.0 - subAction.SlippageTolerancePct
		if toleranceFactor <= 0 {
			return sdkmath.ZeroInt(), errors.New("tolerance factor must be positive")
		}

		// Use decimal arithmetic for precision
		expectedSharesDec := sdkmath.LegacyNewDecFromInt(subAction.ExpectedSharesOut)
		toleranceFactorDec, err := sdkmath.LegacyNewDecFromStr(fmt.Sprintf("%.18f", toleranceFactor))
		if err != nil {
			return sdkmath.ZeroInt(), fmt.Errorf("failed to create tolerance factor decimal: %w", err)
		}

		minSharesDec := expectedSharesDec.Mul(toleranceFactorDec)
		calculatedMinShares := minSharesDec.TruncateInt()

		// Ensure minimum is at least 1
		if calculatedMinShares.IsZero() {
			minShares = sdkmath.NewInt(1)
		} else {
			minShares = calculatedMinShares
		}

		// Validate the result
		if minShares.IsNegative() {
			return sdkmath.ZeroInt(), errors.New("calculated minimum shares is negative")
		}
	}

	return minShares, nil
}

// validateCoin validates a single coin
func validateCoin(coin sdk.Coin, context string) error {
	if coin.Amount.IsNil() {
		return fmt.Errorf("%s: amount is nil", context)
	}
	if coin.Amount.IsZero() {
		return fmt.Errorf("%s: amount cannot be zero", context)
	}
	if coin.Amount.IsNegative() {
		return fmt.Errorf("%s: amount cannot be negative", context)
	}
	if coin.Denom == "" {
		return fmt.Errorf("%s: denomination cannot be empty", context)
	}
	return nil
}

// validateDepositMessage validates a deposit message
func validateDepositMessage(msg *vaulttypes.MsgPerformActionJoinPool) error {
	if msg.Creator == "" {
		return errors.New("deposit message creator cannot be empty")
	}
	if msg.VaultId == 0 {
		return errors.New("deposit message vault ID cannot be zero")
	}
	if msg.PoolId == 0 {
		return errors.New("deposit message pool ID cannot be zero")
	}
	if len(msg.MaxAmountsIn) == 0 {
		return errors.New("deposit message must have at least one amount")
	}
	if msg.ShareAmountOut.IsNil() || msg.ShareAmountOut.IsNegative() {
		return errors.New("deposit message share amount out is invalid")
	}

	// Validate all amounts
	for i, coin := range msg.MaxAmountsIn {
		if err := validateCoin(coin, fmt.Sprintf("deposit message amount %d", i)); err != nil {
			return err
		}
	}

	return nil
}

// createWithdrawLPMessage creates a withdraw LP message with comprehensive validation
func (tb *TransactionBuilder) createWithdrawLPMessage(subAction types.SubAction, vaultID uint64) (sdk.Msg, error) {
	// Validate LP shares to withdraw
	if subAction.LPSharesToWithdraw.IsNil() {
		return nil, errors.Join(ErrInvalidShares, errors.New("LP shares to withdraw is nil"))
	}
	if subAction.LPSharesToWithdraw.IsZero() {
		return nil, errors.Join(ErrInvalidShares, errors.New("LP shares to withdraw cannot be zero"))
	}
	if subAction.LPSharesToWithdraw.IsNegative() {
		return nil, errors.Join(ErrInvalidShares, errors.New("LP shares to withdraw cannot be negative"))
	}

	// Calculate minimum amounts out with proper slippage protection
	minAmountsOut, err := calculateMinimumWithdrawAmounts(subAction)
	if err != nil {
		return nil, errors.Join(ErrMathematicalError, fmt.Errorf("failed to calculate minimum withdraw amounts: %w", err))
	}

	// Create and validate the message
	msg := &vaulttypes.MsgPerformActionExitPool{
		Creator:       tb.signingClient.GetAddressString(),
		VaultId:       vaultID,
		PoolId:        uint64(subAction.PoolIDToWithdraw),
		MinAmountsOut: minAmountsOut,
		ShareAmountIn: subAction.LPSharesToWithdraw,
		TokenOutDenom: subAction.TargetDenomOnExit,
	}

	// Validate message fields
	if err := validateWithdrawMessage(msg); err != nil {
		return nil, err
	}

	txLogger.Info().
		Uint64("poolId", uint64(subAction.PoolIDToWithdraw)).
		Str("sharesToWithdraw", subAction.LPSharesToWithdraw.String()).
		Str("targetDenom", subAction.TargetDenomOnExit).
		Int("minAmountsOutCount", len(minAmountsOut)).
		Float64("slippageProtection", subAction.SlippageTolerancePct*100).
		Float64("expectedSlippage", subAction.ExpectedSlippage*100).
		Msg("Created vault exit pool message with comprehensive slippage protection")

	return msg, nil
}

// validateWithdrawMessage validates a withdraw message
func validateWithdrawMessage(msg *vaulttypes.MsgPerformActionExitPool) error {
	if msg.Creator == "" {
		return errors.New("withdraw message creator cannot be empty")
	}
	if msg.VaultId == 0 {
		return errors.New("withdraw message vault ID cannot be zero")
	}
	if msg.PoolId == 0 {
		return errors.New("withdraw message pool ID cannot be zero")
	}
	if msg.ShareAmountIn.IsNil() || msg.ShareAmountIn.IsZero() || msg.ShareAmountIn.IsNegative() {
		return errors.New("withdraw message share amount in is invalid")
	}

	// Validate minimum amounts out if provided
	for i, coin := range msg.MinAmountsOut {
		if err := validateCoin(coin, fmt.Sprintf("withdraw message min amount %d", i)); err != nil {
			return err
		}
	}

	return nil
}

// validateTxResponse validates transaction response
func validateTxResponse(txResponse *sdk.TxResponse) error {
	if txResponse == nil {
		return errors.New("transaction response is nil")
	}
	if txResponse.TxHash == "" {
		return errors.New("transaction hash is empty")
		}
	// Code 0 means success in Cosmos SDK
	if txResponse.Code != 0 {
		return fmt.Errorf("transaction failed with code %d: %s", txResponse.Code, txResponse.RawLog)
	}
	return nil
	}

// calculateMinimumWithdrawAmounts calculates minimum amounts out for withdrawal with slippage protection
func calculateMinimumWithdrawAmounts(subAction types.SubAction) ([]sdk.Coin, error) {
	if len(subAction.ExpectedAmountsOut) == 0 {
		// Fallback: if no expected amounts, use minimal protection
		if subAction.TargetDenomOnExit != "" {
			return []sdk.Coin{{Denom: subAction.TargetDenomOnExit, Amount: sdkmath.NewInt(1)}}, nil
		}
		return []sdk.Coin{}, nil
}

	// Validate slippage tolerance
	if subAction.SlippageTolerancePct < 0 || subAction.SlippageTolerancePct > 1 {
		return nil, fmt.Errorf("invalid slippage tolerance: %f (must be between 0 and 1)", subAction.SlippageTolerancePct)
	}

	minAmountsOut := make([]sdk.Coin, len(subAction.ExpectedAmountsOut))
	toleranceFactor := 1.0 - subAction.SlippageTolerancePct

	// Validate tolerance factor
	if toleranceFactor < 0 || toleranceFactor > 1 {
		return nil, fmt.Errorf("calculated tolerance factor is invalid: %f", toleranceFactor)
	}

	txLogger.Debug().
		Float64("slippageTolerance", subAction.SlippageTolerancePct).
		Float64("toleranceFactor", toleranceFactor).
		Int("expectedAmountsCount", len(subAction.ExpectedAmountsOut)).
		Msg("Calculating minimum withdrawal amounts with slippage protection")

	for i, expectedCoin := range subAction.ExpectedAmountsOut {
		// Validate expected coin
		if err := validateCoin(expectedCoin, fmt.Sprintf("expected amount %d", i)); err != nil {
			return nil, fmt.Errorf("invalid expected coin %d: %w", i, err)
		}

		// Convert to decimal for precise calculation
		expectedDec := sdkmath.LegacyNewDecFromInt(expectedCoin.Amount)
		toleranceFactorDec, err := sdkmath.LegacyNewDecFromStr(fmt.Sprintf("%.18f", toleranceFactor))
	if err != nil {
			return nil, fmt.Errorf("failed to create tolerance factor decimal: %w", err)
	}

		// Calculate minimum amount with slippage protection
		minAmountDec := expectedDec.Mul(toleranceFactorDec)
		minAmount := minAmountDec.TruncateInt()

		// Ensure minimum amount is at least 1 if expected amount is positive
		if minAmount.IsZero() && expectedCoin.Amount.IsPositive() {
			minAmount = sdkmath.NewInt(1)
			txLogger.Debug().
				Str("denom", expectedCoin.Denom).
				Str("expectedAmount", expectedCoin.Amount.String()).
				Msg("Minimum amount calculated as zero, setting to 1 for dust protection")
		}

		minAmountsOut[i] = sdk.Coin{
			Denom:  expectedCoin.Denom,
			Amount: minAmount,
	}

		txLogger.Debug().
			Str("denom", expectedCoin.Denom).
			Str("expectedAmount", expectedCoin.Amount.String()).
			Str("minAmount", minAmount.String()).
			Float64("protectionPercent", (1.0-toleranceFactor)*100).
			Msg("Calculated minimum withdrawal amount with slippage protection")
	}

	txLogger.Info().
		Int("minAmountsCount", len(minAmountsOut)).
		Float64("slippageProtection", subAction.SlippageTolerancePct*100).
		Msg("Calculated minimum withdrawal amounts with comprehensive slippage protection")

	return minAmountsOut, nil
}

/*

This file contains the types for positions which contains all the state needed for assisting in rebalancing.

*/

package types

import (
	"time"

	sdkmath "cosmossdk.io/math"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
)

// LP position type
type Position struct {
	PoolID         PoolID      `json:"pool_id"`
	LPShares       sdkmath.Int `json:"lp_shares"`                 // The amount of LP shares the vault holds
	AgeDays        int         `json:"age_in_days"`               // The age of the position in days, used for continunity bonus
	EstimatedValue float64     `json:"estimated_value,omitempty"` // Can be populated by vault manager
}

// When the vault is just holding a single token, this is the type for that position
type TokenPosition struct {
	Denom          string      `json:"denom"`           // The denom of the token (ibc/273...A8)
	Symbol         string      `json:"symbol"`          // The symbol of the token (atom)
	Amount         sdkmath.Int `json:"amount"`          // The amount of the token (uatom)
	EstimatedValue float64     `json:"estimated_value"` // The estimated value of the token in USD
}

// SubActionType defines the specific low-level operations.
type SubActionType string

const (
	SubActionSwap       SubActionType = "SWAP"
	SubActionDepositLP  SubActionType = "DEPOSIT_LP"  // Deposit into a liquidity pool
	SubActionWithdrawLP SubActionType = "WITHDRAW_LP" // Withdraw from a liquidity pool
	SubActionNoOp       SubActionType = "NO_OP"       // Placeholder if no action needed for a step
)

// SubAction represents a single, executable step in a rebalancing plan.
type SubAction struct {
	Type SubActionType `json:"type"`

	// Fields for SWAP
	TokenIn       sdktypes.Coin `json:"token_in,omitempty"`         // For SWAP: Coin to swap from
	TokenOutDenom string        `json:"token_out_denom,omitempty"`  // For SWAP: Target denom to swap to
	PoolIDForSwap PoolID        `json:"pool_id_for_swap,omitempty"` // Optional: if swap must happen in a specific pool (0 if any)

	// Fields for DEPOSIT_LP
	PoolIDToDeposit  PoolID          `json:"pool_id_to_deposit,omitempty"` // For DEPOSIT_LP
	AmountsToDeposit []sdktypes.Coin `json:"amounts_to_deposit,omitempty"` // For DEPOSIT_LP: specific token amounts

	// Fields for WITHDRAW_LP
	PoolIDToWithdraw   PoolID      `json:"pool_id_to_withdraw,omitempty"`   // For WITHDRAW_LP
	LPSharesToWithdraw sdkmath.Int `json:"lp_shares_to_withdraw,omitempty"` // For WITHDRAW_LP
	TargetDenomOnExit  string      `json:"target_denom_on_exit,omitempty"`  // For WITHDRAW_LP: if exiting to a single token (e.g., USDC)

	// Simulation results for slippage protection
	ExpectedTokenOut     sdkmath.Int     `json:"expected_token_out,omitempty"`     // For SWAP: expected amount out from simulation
	ExpectedAmountsOut   []sdktypes.Coin `json:"expected_amounts_out,omitempty"`   // For WITHDRAW_LP: expected amounts out from simulation
	ExpectedSlippage     float64         `json:"expected_slippage,omitempty"`      // For SWAP/DEPOSIT_LP/WITHDRAW_LP: expected slippage percentage
	ExpectedSharesOut    sdkmath.Int     `json:"expected_shares_out,omitempty"`    // For DEPOSIT_LP: expected LP shares from simulation
	SlippageTolerancePct float64         `json:"slippage_tolerance_pct,omitempty"` // Maximum acceptable slippage (e.g., 0.05 for 5%)
}

// ActionPlan holds a sequence of SubActions to achieve a rebalancing goal.
type ActionPlan struct {
	GoalDescription       string      `json:"goal_description"` // e.g., "Rebalance to target allocations"
	SubActions            []SubAction `json:"sub_actions"`
	EstimatedNetUSDChange float64     `json:"estimated_net_usd_change"` // For logging/tracking
}

// ActionReceipt needs to be general enough for SubActions or we need specific receipts.
// For now, let's keep ActionReceipt somewhat general and populate relevant fields.
type ActionReceipt struct {
	ReceiptID         int64                  `json:"receipt_id,omitempty"` // Auto-incremented by DB
	OriginalSubAction SubAction              `json:"original_sub_action"`
	Success           bool                   `json:"success"`
	Message           string                 `json:"message,omitempty"`
	Timestamp         time.Time              `json:"timestamp"`
	ResultingCoins    []sdktypes.Coin        `json:"resulting_coins,omitempty"`
	LPSharesChanged   sdkmath.Int            `json:"lp_shares_changed,omitempty"`
	ActualAmountUSD   float64                `json:"actual_amount_usd,omitempty"` // Value of assets moved/received
	TokensDeposited   map[string]sdkmath.Int `json:"tokens_deposited,omitempty"`  // For DEPOSIT_LP
	TokensWithdrawn   map[string]sdkmath.Int `json:"tokens_withdrawn,omitempty"`  // For WITHDRAW_LP
}

// TransactionResult contains all transaction execution details including gas fees
type TransactionResult struct {
	TxHash       string  `json:"tx_hash"`
	GasUsed      int64   `json:"gas_used"`
	GasWanted    int64   `json:"gas_wanted"`
	GasFeeUSD    float64 `json:"gas_fee_usd"`
	Success      bool    `json:"success"`
	ErrorMessage string  `json:"error_message,omitempty"`
}

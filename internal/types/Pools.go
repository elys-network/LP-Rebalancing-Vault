/*

This is a custom type for pools which contains all the state needed for scoring pools

*/

package types

import (
	"cosmossdk.io/math"
)

type PoolID uint64

type Pool struct {
	ID              PoolID   `json:"id"`               // e.g., "ATOM-USDC"
	TokenA          Token    `json:"token_a"`          // e.g., ATOM (non-USDC)
	TokenB          Token    `json:"token_b"`          // e.g., Likely USDC
	BalanceA        math.Int `json:"balance_a"`        // Amount of TokenA (T)
	BalanceB        math.Int `json:"balance_b"`        // Amount of TokenB (USDC)
	WeightA         float64  `json:"weight_a"`         // Weight of TokenA (normalized, 0.0 to 1.0)
	WeightB         float64  `json:"weight_b"`         // Weight of TokenB (normalized, 0.0 to 1.0)
	TvlUSD          float64  `json:"tvl_usd"`          // Total Value Locked in USD
	Volume7dUSD     float64  `json:"volume_7d_usd"`    // 7-day Trading Volume in USD
	EdenRewardsAPR  float64  `json:"eden_rewards_apr"` // EDEN rewards component
	UsdcFeesAPR     float64  `json:"usdc_fees_apr"`    // USDC fees component
	PriceImpactAPR  float64  `json:"price_impact_apr"` // Price impact fee component
	IsSmartShielded bool     `json:"is_smart_shielded"`
	AgeInDays       int      `json:"age_in_days"`
	SwapFee         float64  `json:"swap_fee"`
	SentimentScore  float64  `json:"sentiment_score,omitempty"` // -1 to +1, optional
	TotalShares     math.Int `json:"total_shares"`              // Total number of shares in the pool

	Score PoolScoreResult `json:"score"`

	// Internal state for tracking vault position
	HasCurrentPosition     bool    `json:"-"`
	CurrentPositionAgeDays int     `json:"-"`
	EstimatedPositionValue float64 `json:"-"` // Current value of the vault's position in this pool
}

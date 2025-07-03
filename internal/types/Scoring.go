/*

This file contains the types for scoring pools, and other configuarable parameters for the AVM.

*/

package types

// ScoringParameters holds all tunable weights, coefficients, and thresholds
// used by the AVM strategy for scoring, allocation, and execution logic.
// Different sets of these parameters can exist for different market regimes.
type ScoringParameters struct {
	// --- General Strategy Parameters ---
	MaxPools                   int     `json:"max_pools"`                     // Maximum number of pools the AVM will consider investing in.
	MinAllocation              float64 `json:"min_allocation"`                // Minimum percentage of total vault value to allocate to a single selected pool.
	MaxAllocation              float64 `json:"max_allocation"`                // Maximum percentage of total vault value to allocate to a single selected pool.
	RebalanceThresholdAmount   float64 `json:"rebalance_threshold_amount"`    // Minimum percentage change required to trigger a rebalance action for a pool (e.g., 5.0 for 5%).
	MaxRebalancePercentPerCycle float64 `json:"max_rebalance_percent_per_cycle"` // Maximum percentage of total vault value that can be withdrawn from pools per cycle (e.g., 5.0 for 5%). Does not limit deposits.
	MinLiquidUSDCBuffer        float64 `json:"min_liquid_usdc_buffer"`        // Minimum amount of USDC to keep liquid in the vault (not invested).
	SmartShieldSlippagePercent float64 `json:"smart_shield_slippage_percent"` // Maximum price impact (as a percentage, e.g., 1.0 for 1%) allowed for SmartShielded pools.
	NormalPoolSlippagePercent  float64 `json:"normal_pool_slippage_percent"`  // Maximum price impact (as a percentage, e.g., 3.0 for 3%) allowed for normal pools.
	ViableSwapReductionFactor  float64 `json:"viable_swap_reduction_factor"`  // Factor by which to reduce the swap amount when trying to find a viable swap.
	ViableDepositReductionFactor float64 `json:"viable_deposit_reduction_factor"` // Factor by which to reduce the deposit amount when trying to find a viable deposit.

	// --- Reward Score Components ---
	AprCoefficient           float64 `json:"apr_coefficient"`            // Coefficient for the weighted APR's impact on the reward score.
	TradingVolumeCoefficient float64 `json:"trading_volume_coefficient"` // Coefficient for the 7-day trading volume's impact on the reward score (log10 scaled).

	// APR Weights (should ideally sum to 1.0, or be normalized)
	EdenWeight        float64 `json:"eden_weight"`         // Weight for Eden rewards APR component.
	UsdcFeeWeight     float64 `json:"usdc_fee_weight"`     // Weight for USDC swap fees APR component.
	PriceImpactWeight float64 `json:"price_impact_weight"` // Weight for Price Impact savings APR component.

	// --- Risk Score Components ---
	IlRiskCoefficient     float64 `json:"il_risk_coefficient"`    // Coefficient for Impermanent Loss risk (typically negative).
	VolatilityCoefficient float64 `json:"volatility_coefficient"` // Coefficient for Token A's volatility (typically negative).
	NewPoolCoefficient    float64 `json:"new_pool_coefficient"`   // Penalty coefficient for new pools (typically negative).
	PoolMaturityDays      int     `json:"pool_maturity_days"`     // Number of days after which a pool is no longer considered "new" and the penalty is fully removed.

	// IL Risk Specifics
	IlHoldingPeriodYears       float64 `json:"il_holding_period_years"`       // Assumed holding period in years for IL calculation.
	IlConfidenceFactor         float64 `json:"il_confidence_factor"`          // Factor to scale IL risk (e.g., >1 for conservatism).
	SmartShieldReductionFactor float64 `json:"smart_shield_reduction_factor"` // Percentage reduction in IL risk if SmartShield is active (0.0 to 1.0).

	// --- Liquidity Score Components ---
	TvlCoefficient  float64 `json:"tvl_coefficient"`   // Coefficient for TVL's impact on the liquidity score (log10 scaled).
	MinTVLThreshold float64 `json:"min_tvl_threshold"` // Minimum TVL in USD to consider for log scaling (e.g., to avoid log(0) or very small numbers).

	// --- Bonus Score Components ---
	SmartShieldBonus       float64 `json:"smart_shield_bonus"`       // Flat bonus score if a pool has SmartShield.
	ContinuityCoefficient  float64 `json:"continuity_coefficient"`   // Coefficient for the continuity bonus (scaled by position age).
	ContinuityLookbackDays int     `json:"continuity_lookback_days"` // Max number of days to consider for scaling the continuity bonus.

	// --- Sentiment/External Factor Component ---
	SentimentImpactFactor float64 `json:"sentiment_impact_factor"` // Factor to scale external sentiment score (can be positive or negative).

	// --- Optimization & Learning Parameters (Placeholder - more complex logic needed here) ---
	// These define how the AVM might adjust its own parameters over time.
	OptimizationIntervalCycles int     `json:"optimization_interval_cycles"` // Number of AVM cycles before an optimization/learning step is considered.
	LearningRate               float64 `json:"learning_rate"`                // General learning rate for parameter adjustments.
	MaxParameterChange         float64 `json:"max_parameter_change"`         // Max percentage change for a single parameter during one optimization step.
	// Add more specific learning parameters as needed, e.g., for individual coefficients.

	// --- ELYS Protocol Parameters ---
	ElysForcedAllocationMinimum float64 `json:"elys_forced_allocation_minimum"` // Minimum allocation percentage for ELYS pools (e.g., 0.10 for 10%). The ELYS pool will always receive at least this allocation, even if not in top MaxPools.
}

type PoolScoreResult struct {
	PoolID     PoolID  `json:"pool_id"`
	Score      float64 `json:"final_score"`
	Components struct {
		WeightedAPR          float64 `json:"weighted_apr"`
		ILRisk               float64 `json:"il_risk"`
		AnnualizedVolatility float64 `json:"annualized_volatility"`
		RewardScoreComponent float64 `json:"reward_score_component"`
		RiskScoreComponent   float64 `json:"risk_score_component"`
		TvlScoreComponent    float64 `json:"tvl_score_component"`
		BonusScoreComponent  float64 `json:"bonus_score_component"`
		SentimentAdjustment  float64 `json:"sentiment_adjustment,omitempty"`
	} `json:"components"`
}

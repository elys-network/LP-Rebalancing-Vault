/*

This file contains the default parameters for the AVM.

These parameters are designed for managing significant capital (millions of dollars) in a production environment.
Each value has been carefully chosen to balance risk management with return optimization.

*/

package config

import (
	"github.com/elys-network/avm/internal/types"
)

// DefaultScoringParameters provides a baseline set of parameters for the AVM's scoring logic.
// These values are used if no active parameters are found in the database during initialization.
//
// IMPORTANT: These defaults are calibrated for managing large amounts of capital (>$1M).
// They prioritize capital preservation and risk management over aggressive yield chasing.
var DefaultScoringParameters = types.ScoringParameters{
	// --- General Strategy Parameters ---
	MaxPools: 5, // Consider top 5 pools for optimal diversification (including forced ELYS pool).
	// Rationale: With millions at stake, concentration risk is the primary threat.
	// 4 pools provides meaningful diversification while remaining manageable.
	// Each additional pool reduces the impact of any single pool failure.

	MinAllocation: 0.08, // Allocate at least 8% to a selected pool.
	// Rationale: Allocations below 8% lead to over-diversification and high transaction costs.
	// 8% ensures each position is meaningful while maintaining diversification.
	// Below this threshold, positions may be too small to justify costs for rebalancing.

	MaxAllocation: 0.35, // Allocate at most 35% to a single pool.
	// Rationale: Higher concentration creates catastrophic risk for large capital.
	// If a pool suffers an exploit or major IL event, losses are contained to 35%.
	// This provides meaningful risk reduction while allowing substantial positions.

	RebalanceThresholdAmount: 5.0, // Rebalance if deviation exceeds 2%.
	// Rationale: With large positions, small percentage deviations represent significant dollar amounts.
	// A 5% threshold on a $10M vault is $500k - substantial enough to warrant rebalancing.
	// More responsive rebalancing captures opportunities and maintains target risk profile.

	MaxRebalancePercentPerCycle: 10.0, // Limit withdrawals to 10% of vault per cycle.
	// Rationale: Large withdrawals create significant slippage and market impact.

	MinLiquidUSDCBuffer: 50.0, // Keep at least $50 USDC liquid.
	// Rationale: A buffer incase of percision errors

	SmartShieldSlippagePercent: 1.0, // Allow up to 1% slippage for SmartShielded pools.
	// Rationale: With large positions, even small slippage represents significant costs.
	// SmartShielded pools justify slightly higher slippage due to IL protection.

	NormalPoolSlippagePercent: 3, // Allow up to 3% slippage for normal pools.
	// Rationale: Strict slippage control is essential for large position management.
	// Forces the AVM to wait for better market conditions rather than accept high costs.
	// Better to skip a trade than lose significant capital to slippage.

	ViableSwapReductionFactor: 0.9, // Reduce swap amounts by 10% when slippage is too high.
	// Rationale: Conservative approach to finding executable swaps under slippage constraints.
	// 90% of original amount often provides sufficient reduction to meet slippage limits.

	ViableDepositReductionFactor: 0.8, // Reduce deposit amounts by 20% when slippage is too high.
	// Rationale: Deposits typically require more conservative sizing due to multi-token complexity.
	// 80% provides meaningful reduction while maintaining substantial position entry.

	// --- APR Weights (Adjusted for Risk-Adjusted Returns) ---
	EdenWeight: 0.8, // Weight for EDEN rewards component.
	// Rationale: EDEN rewards are paid in a volatile token, not stable value.
	// Large positions amplify this volatility risk. Weighting reflects this uncertainty.

	UsdcFeeWeight: 1.5, // Weight for USDC fees component - prioritize stable income.
	// Rationale: USDC fees are the most stable component of pool returns.
	// For large capital, predictable income is more valuable than volatile rewards.
	// Higher weighting favors pools with strong, stable fee generation.

	PriceImpactWeight: 1.3, // Weight for price impact savings - crucial at scale.
	// Rationale: Large trades suffer more from price impact. Pools that reduce this
	// through better mechanisms (like price impact rebates) become more valuable.

	// --- Scoring Coefficients (Risk-Adjusted for Large Capital) ---
	AprCoefficient: 0.8, // Coefficient for APR impact on score.
	// Rationale: High APR often correlates with high risk. Large capital should prioritize
	// risk-adjusted returns over absolute yield. Better to earn 15% safely than 25% dangerously.

	TradingVolumeCoefficient: 0.5, // Coefficient for volume impact - critical for liquidity.
	// Rationale: Large positions need deep liquidity to enter/exit without massive slippage.
	// Volume is the best proxy for liquidity depth. Higher weighting favors liquid pools.

	IlRiskCoefficient: -1.2, // Penalty coefficient for IL risk - scales with position size.
	// Rationale: A 10% IL on $100k is $10k. A 10% IL on $10M is $1M.
	// IL risk becomes exponentially more dangerous with larger positions.

	VolatilityCoefficient: -1.0, // Penalty coefficient for volatility - compounds risk.
	// Rationale: Volatility creates both IL risk and emotional/operational stress.
	// Large positions in volatile assets can create massive daily swings that affect decision-making.

	NewPoolCoefficient: -8.0, // Strong penalty for new pools - extremely risky.
	// Rationale: Most DeFi exploits happen in new, unproven protocols.
	// Large capital cannot afford to be early adopters. Let others test new pools first.

	TvlCoefficient: 0.6, // Coefficient for TVL impact - critical for large positions.
	// Rationale: Large positions need pools with substantial TVL to avoid dominating them.
	// A $1M position in a $5M pool is 20% - too concentrated and illiquid.

	SmartShieldBonus: 8.0, // Bonus for SmartShielded pools - invaluable protection.
	// Rationale: Smart Shield protection becomes exponentially more valuable with larger positions.
	// The absolute dollar value of IL protection scales directly with position size.

	ContinuityCoefficient: 2.0, // Bonus for maintaining existing positions.
	// Rationale: While continuity reduces transaction costs, it shouldn't prevent
	// necessary rebalancing. Large capital needs to be dynamic, not overly sticky.

	SentimentImpactFactor: -0.3, // Factor for sentiment adjustment.
	// Rationale: Sentiment can be manipulated and is often wrong. Large capital should
	// rely more on fundamental metrics than market sentiment.

	// --- IL Risk Calculation Parameters ---
	IlConfidenceFactor: 2.5, // Conservative factor for IL risk estimation.
	// Rationale: IL calculations are estimates. With large capital, it's better to
	// overestimate IL risk than underestimate it. Conservative assumptions protect capital.

	IlHoldingPeriodYears: 30.0 / 365.0, // 30-day holding period for IL calculations.
	// Rationale: Large positions may take longer to unwind, extending IL exposure.
	// 30 days provides a realistic assessment of IL risk for substantial positions.

	SmartShieldReductionFactor: 0.15, // Conservative Smart Shield effectiveness assumption.
	// Rationale: Smart Shield is helpful but not perfect. Conservative assumption
	// ensures the AVM doesn't become over-dependent on this protection mechanism.

	// --- Other Configuration ---
	MinTVLThreshold: 50000, // Minimum $50k TVL required for pool consideration.
	// Rationale: Pools under $50k TVL cannot accommodate large positions without
	// dominating them. This threshold ensures adequate liquidity for meaningful positions.

	PoolMaturityDays: 30, // Require 30 days of operation before considering pools.
	// Rationale: Most pool exploits happen in the first 30 days. Large capital
	// should wait for pools to prove their security and stability over time.

	ContinuityLookbackDays: 30, // 30-day period for assessing position continuity.
	// Rationale: Adequate time period to assess whether maintaining a position
	// provides meaningful benefits over the transaction costs of rebalancing.

	// --- Optimization & Learning Parameters ---
	OptimizationIntervalCycles: 288, // Optimize parameters every 2 days (288 cycles).
	// Rationale: Measured approach to parameter evolution allows adequate data collection.
	// Stability is more important than rapid adaptation when managing millions.
	// Prevents over-optimization and maintains strategy consistency.

	LearningRate: 0.005, // Conservative learning rate for parameter adjustments.
	// Rationale: Large capital requires extremely gradual parameter evolution.
	// Rapid changes could destabilize a working strategy. Better to evolve slowly and safely.

	MaxParameterChange: 0.05, // Limit parameter changes to 5% per optimization cycle.
	// Rationale: Prevents the optimization system from making radical strategy shifts.
	// Ensures parameter evolution remains gradual and doesn't destabilize performance.

	// --- ELYS Protocol Parameters ---
	ElysForcedAllocationMinimum: 0.10, // Ensure ELYS pools always receive at least 10% allocation
	// Rationale: As the protocol's native asset, ELYS pools should always have meaningful exposure.
	// This guarantees support for the protocol while maintaining diversification with other assets.
}
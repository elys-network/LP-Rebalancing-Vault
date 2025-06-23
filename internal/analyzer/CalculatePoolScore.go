/*

This file contains the main function for calculating the score for a pool.

*/

package analyzer

import (
	"errors"
	"fmt"
	"math"

	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types"
)

var ErrInsufficientDataVolatility = errors.New("insufficient data points to calculate volatility")
var ErrInvalidPoolData = errors.New("invalid pool data")
var ErrInvalidScoringParameters = errors.New("invalid scoring parameters")
var scoreLogger = logger.GetForComponent("pool_scorer")

// CalculatePoolScore calculates the final score for a pool by orchestrating calls
// to modular component calculation functions and summing their results.
// It requires a fully populated Pool struct (including vault-specific
// state like HasCurrentPosition) and the ScoringParameters.
// Inputs:
//   - pool: The pool data structure, potentially augmented with vault position state.
//   - params: The scoring parameters defining weights, coefficients, and thresholds.
//
// Output:
//   - A PoolScoreResult containing the final score and component breakdown.
//   - An error if essential calculations cannot be performed or validation fails.
func CalculatePoolScore(pool types.Pool, params types.ScoringParameters) (types.PoolScoreResult, error) {
	// Validate input data before performing calculations
	if err := ValidatePoolData(pool); err != nil {
		scoreLogger.Error().
			Uint64("poolID", uint64(pool.ID)).
			Err(err).
			Msg("Pool data validation failed")
		return types.PoolScoreResult{}, errors.Join(ErrInvalidPoolData, err)
	}

	if err := ValidateScoringParameters(params); err != nil {
		scoreLogger.Error().
			Uint64("poolID", uint64(pool.ID)).
			Err(err).
			Msg("Scoring parameters validation failed")
		return types.PoolScoreResult{}, errors.Join(ErrInvalidScoringParameters, err)
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Msg("Pool and parameter validation passed, proceeding with score calculation")

	//  Initialize Result Struct
	// Pre-populate known values like ID and volatility
	result := types.PoolScoreResult{
		PoolID: pool.ID,
		Components: struct {
			WeightedAPR          float64 `json:"weighted_apr"`
			ILRisk               float64 `json:"il_risk"`
			AnnualizedVolatility float64 `json:"annualized_volatility"`
			RewardScoreComponent float64 `json:"reward_score_component"`
			RiskScoreComponent   float64 `json:"risk_score_component"`
			TvlScoreComponent    float64 `json:"tvl_score_component"`
			BonusScoreComponent  float64 `json:"bonus_score_component"`
			SentimentAdjustment  float64 `json:"sentiment_adjustment,omitempty"`
		}{
			AnnualizedVolatility: pool.TokenA.Volatility, // Store the base volatility
		},
	}

	//  Calculate Reward Components
	weightedAPR, err := CalculateWeightedAPR(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("weighted APR calculation failed"), err)
	}
	result.Components.WeightedAPR = weightedAPR

	rewardScoreComponent, err := CalculateRewardScore(weightedAPR, pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("reward score calculation failed"), err)
	}
	result.Components.RewardScoreComponent = rewardScoreComponent

	//  Calculate Risk Components
	agePenalty, err := CalculateAgePenalty(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("age penalty calculation failed"), err)
	}

	sentimentAdjustment, err := CalculateSentimentAdjustment(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("sentiment adjustment calculation failed"), err)
	}
	result.Components.SentimentAdjustment = sentimentAdjustment

	ilRisk, err := CalculateILRisk(pool.TokenA.Volatility, pool.IsSmartShielded, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("IL risk calculation failed"), err)
	}
	result.Components.ILRisk = ilRisk

	riskScoreComponent, err := CalculateRiskScore(ilRisk, agePenalty, sentimentAdjustment, pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("risk score calculation failed"), err)
	}
	result.Components.RiskScoreComponent = riskScoreComponent

	//  Calculate Liquidity Component
	liquidityScoreComponent, err := CalculateLiquidityScore(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("liquidity score calculation failed"), err)
	}
	result.Components.TvlScoreComponent = liquidityScoreComponent

	//  Calculate Bonus Components
	shieldBonus, err := CalculateSmartShieldBonus(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("smart shield bonus calculation failed"), err)
	}

	continuityBonus, err := CalculateContinuityBonus(pool, params)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("continuity bonus calculation failed"), err)
	}

	bonusScoreComponent, err := CalculateTotalBonusScore(shieldBonus, continuityBonus)
	if err != nil {
		return types.PoolScoreResult{}, errors.Join(errors.New("total bonus score calculation failed"), err)
	}
	result.Components.BonusScoreComponent = bonusScoreComponent

	//  Calculate Final Score
	// Sum the main components (Risk is typically negative, Bonuses positive)
	finalScore := rewardScoreComponent + riskScoreComponent + liquidityScoreComponent + bonusScoreComponent
	result.Score = finalScore

	// Validate final score is reasonable - CRITICAL for financial safety
	if math.IsNaN(finalScore) || math.IsInf(finalScore, 0) {
		scoreLogger.Error().
			Uint64("poolID", uint64(pool.ID)).
			Float64("finalScore", finalScore).
			Msg("Final score calculation resulted in invalid value")
		return types.PoolScoreResult{}, errors.New("final score calculation resulted in NaN or Inf")
	}

	// Additional safety check - ensure all components are finite
	components := []struct {
		value float64
		name  string
	}{
		{rewardScoreComponent, "reward component"},
		{riskScoreComponent, "risk component"},
		{liquidityScoreComponent, "liquidity component"},
		{bonusScoreComponent, "bonus component"},
	}

	for _, comp := range components {
		if math.IsNaN(comp.value) || math.IsInf(comp.value, 0) {
			scoreLogger.Error().
				Uint64("poolID", uint64(pool.ID)).
				Float64("componentValue", comp.value).
				Str("componentName", comp.name).
				Msg("Score component calculation resulted in invalid value")
			return types.PoolScoreResult{}, errors.New(comp.name + " calculation resulted in NaN or Inf")
		}
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Float64("finalScore", finalScore).
		Float64("rewardComponent", rewardScoreComponent).
		Float64("riskComponent", riskScoreComponent).
		Float64("liquidityComponent", liquidityScoreComponent).
		Float64("bonusComponent", bonusScoreComponent).
		Msg("Pool score calculated")

	return result, nil
}

// CalculateSmartShieldBonus determines the bonus score for smart-shielded pools.
// Returns error for any invalid conditions rather than making assumptions.
func CalculateSmartShieldBonus(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate bonus parameter is finite
	if math.IsNaN(params.SmartShieldBonus) || math.IsInf(params.SmartShieldBonus, 0) {
		return 0, errors.New("SmartShieldBonus parameter is not finite")
	}

	if pool.IsSmartShielded {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Bool("isSmartShielded", true).
			Float64("bonus", params.SmartShieldBonus).
			Msg("Smart shield bonus applied")
		return params.SmartShieldBonus, nil
	} else {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Bool("isSmartShielded", false).
			Msg("No smart shield bonus applied")
		return 0.0, nil
	}
}

func CalculateRewardScore(weightedAPR float64, pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Strict validation - no assumptions allowed
	if math.IsNaN(weightedAPR) || math.IsInf(weightedAPR, 0) {
		return 0, errors.New("weighted APR is not finite")
	}

	// Calculate the score contribution from the weighted APR
	aprScorePart := params.AprCoefficient * weightedAPR

	// Validate APR score part is finite
	if math.IsNaN(aprScorePart) || math.IsInf(aprScorePart, 0) {
		return 0, errors.New("APR score component calculation resulted in non-finite value")
	}

	// Volume must be non-negative - no assumptions about fixing negative values
	if pool.Volume7dUSD < 0 {
		return 0, errors.New("volume cannot be negative")
	}

	// Handle zero volume gracefully - use only APR component
	var volumeScorePart float64
	var logVolume float64

	if pool.Volume7dUSD == 0 {
		// Zero volume means no volume contribution to score
		volumeScorePart = 0.0
		logVolume = 0.0 // For logging purposes

		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Str("tokenA", pool.TokenA.Symbol).
			Str("tokenB", pool.TokenB.Symbol).
			Msg("Pool has zero volume - using only APR component for reward score")
	} else {
		// Calculate volume component for non-zero volume
		logVolume = math.Log10(pool.Volume7dUSD)
		if math.IsNaN(logVolume) || math.IsInf(logVolume, 0) {
			return 0, errors.New("log volume calculation resulted in non-finite value")
		}

		volumeScorePart = params.TradingVolumeCoefficient * logVolume
		if math.IsNaN(volumeScorePart) || math.IsInf(volumeScorePart, 0) {
			return 0, errors.New("volume score component calculation resulted in non-finite value")
		}
	}

	totalRewardScore := aprScorePart + volumeScorePart
	if math.IsNaN(totalRewardScore) || math.IsInf(totalRewardScore, 0) {
		return 0, errors.New("total reward score calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Float64("inputWeightedAPR", weightedAPR).
		Float64("aprCoefficient", params.AprCoefficient).
		Float64("aprScoreComponent", aprScorePart).
		Float64("rawVolume7dUSD", pool.Volume7dUSD).
		Float64("log10VolumeUsed", logVolume).
		Float64("volumeCoefficient", params.TradingVolumeCoefficient).
		Float64("volumeScoreComponent", volumeScorePart).
		Float64("totalRewardScore", totalRewardScore).
		Bool("hasVolume", pool.Volume7dUSD > 0).
		Msg("Reward score calculated with components")

	return totalRewardScore, nil
}

// CalculateContinuityBonus computes the bonus for maintaining an existing position in a pool.
// Returns error for any invalid conditions rather than making assumptions.
func CalculateContinuityBonus(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Check if there's an existing position
	if !pool.HasCurrentPosition {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Msg("No current position, no continuity bonus")
		return 0.0, nil
	}

	// Strict validation - no assumptions or fallbacks
	if params.ContinuityLookbackDays <= 0 {
		return 0, errors.New("ContinuityLookbackDays must be positive")
	}

	if pool.CurrentPositionAgeDays < 0 {
		return 0, errors.New("CurrentPositionAgeDays cannot be negative when position exists")
	}

	// Validate coefficient is finite
	if math.IsNaN(params.ContinuityCoefficient) || math.IsInf(params.ContinuityCoefficient, 0) {
		return 0, errors.New("ContinuityCoefficient is not finite")
	}

	// Calculate bonus scale
	scale := math.Min(1.0, float64(pool.CurrentPositionAgeDays)/float64(params.ContinuityLookbackDays))
	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return 0, errors.New("scale calculation resulted in non-finite value")
	}

	continuityBonus := params.ContinuityCoefficient * scale
	if math.IsNaN(continuityBonus) || math.IsInf(continuityBonus, 0) {
		return 0, errors.New("continuity bonus calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Int("positionAge", pool.CurrentPositionAgeDays).
		Int("lookbackDays", params.ContinuityLookbackDays).
		Float64("scale", scale).
		Float64("bonus", continuityBonus).
		Float64("coefficient", params.ContinuityCoefficient).
		Msg("Continuity bonus calculated")

	return continuityBonus, nil
}

// CalculateTotalBonusScore sums all individual bonus components calculated previously.
// Returns error for any invalid conditions rather than making assumptions.
func CalculateTotalBonusScore(shieldBonus float64, continuityBonus float64) (float64, error) {
	// Validate inputs are finite
	if math.IsNaN(shieldBonus) || math.IsInf(shieldBonus, 0) {
		return 0, errors.New("shield bonus is not finite")
	}
	if math.IsNaN(continuityBonus) || math.IsInf(continuityBonus, 0) {
		return 0, errors.New("continuity bonus is not finite")
	}

	totalBonus := shieldBonus + continuityBonus
	if math.IsNaN(totalBonus) || math.IsInf(totalBonus, 0) {
		return 0, errors.New("total bonus calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Float64("shieldBonus", shieldBonus).
		Float64("continuityBonus", continuityBonus).
		Float64("totalBonus", totalBonus).
		Msg("Total bonus score calculated")

	return totalBonus, nil
}

// CalculateWeightedAPR computes a weighted APR based on the components in the pool data.
// Inputs:
//   - pool: The pool data containing various APR components.
//   - params: The scoring parameters containing weights for different APR components.
//
// Output:
//   - The calculated weighted APR value.
func CalculateWeightedAPR(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate all APR components are finite
	if math.IsNaN(pool.EdenRewardsAPR) || math.IsInf(pool.EdenRewardsAPR, 0) {
		return 0, errors.New("EdenRewardsAPR is not finite")
	}
	if math.IsNaN(pool.UsdcFeesAPR) || math.IsInf(pool.UsdcFeesAPR, 0) {
		return 0, errors.New("UsdcFeesAPR is not finite")
	}
	if math.IsNaN(pool.PriceImpactAPR) || math.IsInf(pool.PriceImpactAPR, 0) {
		return 0, errors.New("PriceImpactAPR is not finite")
	}

	// Validate weight parameters are finite
	if math.IsNaN(params.EdenWeight) || math.IsInf(params.EdenWeight, 0) {
		return 0, errors.New("EdenWeight is not finite")
	}
	if math.IsNaN(params.UsdcFeeWeight) || math.IsInf(params.UsdcFeeWeight, 0) {
		return 0, errors.New("UsdcFeeWeight is not finite")
	}
	if math.IsNaN(params.PriceImpactWeight) || math.IsInf(params.PriceImpactWeight, 0) {
		return 0, errors.New("PriceImpactWeight is not finite")
	}

	// Calculate weighted components
	edenComponent := pool.EdenRewardsAPR * params.EdenWeight
	if math.IsNaN(edenComponent) || math.IsInf(edenComponent, 0) {
		return 0, errors.New("eden component calculation resulted in non-finite value")
	}

	usdcComponent := pool.UsdcFeesAPR * params.UsdcFeeWeight
	if math.IsNaN(usdcComponent) || math.IsInf(usdcComponent, 0) {
		return 0, errors.New("usdc component calculation resulted in non-finite value")
	}

	impactComponent := pool.PriceImpactAPR * params.PriceImpactWeight
	if math.IsNaN(impactComponent) || math.IsInf(impactComponent, 0) {
		return 0, errors.New("price impact component calculation resulted in non-finite value")
	}

	// Strict validation - weights must be positive
	totalWeight := params.EdenWeight + params.UsdcFeeWeight + params.PriceImpactWeight
	if totalWeight <= 0 {
		return 0, errors.New("total APR weight is non-positive, cannot calculate weighted average")
	}
	if math.IsNaN(totalWeight) || math.IsInf(totalWeight, 0) {
		return 0, errors.New("total weight calculation resulted in non-finite value")
	}

	weightedAPR := (edenComponent + usdcComponent + impactComponent) / totalWeight
	if math.IsNaN(weightedAPR) || math.IsInf(weightedAPR, 0) {
		return 0, errors.New("weighted APR calculation resulted in non-finite value")
	}

	//  Optional Debugging
	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Float64("rawEdenAPR", pool.EdenRewardsAPR).
		Float64("edenWeight", params.EdenWeight).
		Float64("weightedEdenComponent", edenComponent).
		Float64("rawUsdcFeesAPR", pool.UsdcFeesAPR).
		Float64("usdcFeeWeight", params.UsdcFeeWeight).
		Float64("weightedUsdcComponent", usdcComponent).
		Float64("rawPriceImpactAPR", pool.PriceImpactAPR).
		Float64("priceImpactWeight", params.PriceImpactWeight).
		Float64("weightedPriceImpactComponent", impactComponent).
		Float64("totalWeight", totalWeight).
		Float64("finalWeightedAPR", weightedAPR).
		Msg("Weighted APR calculated with components")

	return weightedAPR, nil
}

// CalculateRiskScore computes the risk component of the pool's score.
// Returns error for any invalid conditions rather than making assumptions.
func CalculateRiskScore(ilRisk float64, agePenalty float64, sentimentAdjustment float64, pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate all inputs are finite
	inputs := []struct {
		value float64
		name  string
	}{
		{ilRisk, "IL risk"},
		{agePenalty, "age penalty"},
		{sentimentAdjustment, "sentiment adjustment"},
	}

	for _, input := range inputs {
		if math.IsNaN(input.value) || math.IsInf(input.value, 0) {
			return 0, errors.New(input.name + " is not finite")
		}
	}
	// Strict validation - coefficients should typically be negative for risk penalties,
	// but we'll validate they're finite rather than assuming their sign
	if math.IsNaN(params.IlRiskCoefficient) || math.IsInf(params.IlRiskCoefficient, 0) {
		return 0, errors.New("IlRiskCoefficient is not finite")
	}
	if math.IsNaN(params.VolatilityCoefficient) || math.IsInf(params.VolatilityCoefficient, 0) {
		return 0, errors.New("VolatilityCoefficient is not finite")
	}

	// Calculate components
	ilPenalty := params.IlRiskCoefficient * ilRisk
	if math.IsNaN(ilPenalty) || math.IsInf(ilPenalty, 0) {
		return 0, errors.New("IL penalty calculation resulted in non-finite value")
	}

	volatilityPenalty := params.VolatilityCoefficient * pool.TokenA.Volatility
	if math.IsNaN(volatilityPenalty) || math.IsInf(volatilityPenalty, 0) {
		return 0, errors.New("volatility penalty calculation resulted in non-finite value")
	}

	totalRiskScore := ilPenalty + volatilityPenalty + agePenalty + sentimentAdjustment
	if math.IsNaN(totalRiskScore) || math.IsInf(totalRiskScore, 0) {
		return 0, errors.New("total risk score calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Float64("inputIlRisk", ilRisk).
		Float64("ilRiskCoefficient", params.IlRiskCoefficient).
		Float64("ilPenaltyComponent", ilPenalty).
		Float64("inputTokenAVolatility", pool.TokenA.Volatility).
		Float64("volatilityCoefficient", params.VolatilityCoefficient).
		Float64("volatilityPenaltyComponent", volatilityPenalty).
		Float64("inputAgePenalty", agePenalty).
		Float64("inputSentimentAdjustment", sentimentAdjustment).
		Float64("totalRiskScore", totalRiskScore).
		Msg("Risk score calculated with components")

	return totalRiskScore, nil
}

// CalculateLiquidityScore computes the liquidity component of the pool's score.
// Inputs:
//   - pool: The pool data containing TVL information.
//   - params: The scoring parameters containing liquidity-related thresholds and coefficients.
//
// Output:
//   - The calculated liquidity score (positive, as higher liquidity is better).
func CalculateLiquidityScore(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate parameters
	if math.IsNaN(params.MinTVLThreshold) || math.IsInf(params.MinTVLThreshold, 0) {
		return 0, errors.New("MinTVLThreshold is not finite")
	}
	if math.IsNaN(params.TvlCoefficient) || math.IsInf(params.TvlCoefficient, 0) {
		return 0, errors.New("TvlCoefficient is not finite")
	}
	if math.IsNaN(pool.TvlUSD) || math.IsInf(pool.TvlUSD, 0) {
		return 0, errors.New("pool TVL is not finite")
	}

	// Strict validation - MinTVLThreshold must be positive
	if params.MinTVLThreshold <= 0 {
		return 0, errors.New("MinTVLThreshold must be positive")
	}

	// TVL must be positive for meaningful scoring
	if pool.TvlUSD <= 0 {
		return 0, errors.New("pool TVL must be positive")
	}

	// Use the larger of threshold or actual TVL
	logInput := math.Max(params.MinTVLThreshold, pool.TvlUSD)
	if logInput <= 0 {
		return 0, errors.New("log input must be positive")
	}

	logTvl := math.Log10(logInput)
	if math.IsNaN(logTvl) || math.IsInf(logTvl, 0) {
		return 0, errors.New("log TVL calculation resulted in non-finite value")
	}

	liquidityScore := params.TvlCoefficient * logTvl
	if math.IsNaN(liquidityScore) || math.IsInf(liquidityScore, 0) {
		return 0, errors.New("liquidity score calculation resulted in non-finite value")
	}

	//  Optional Debugging
	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Float64("rawTvlUSD", pool.TvlUSD).
		Float64("effectiveMinTvlThreshold", params.MinTVLThreshold).
		Float64("log10InputForTvl", logInput).
		Float64("log10TvlValue", logTvl).
		Float64("tvlCoefficient", params.TvlCoefficient).
		Float64("finalLiquidityScore", liquidityScore).
		Msg("Liquidity score calculated with components")

	return liquidityScore, nil
}

// CalculateILRisk estimates impermanent loss risk based on token volatility.
// It can incorporate a reduction factor for pools that have smart shield protection.
// Inputs:
//   - annualizedVolatility: Token A's annualized volatility (decimal, e.g., 0.4 for 40%).
//   - isSmartShielded: Boolean flag indicating if the pool has smart shield protection.
//   - params: The scoring parameters containing IL risk factors.
//
// Output:
//   - The calculated IL risk score (typically positive, as it will be multiplied by a negative coefficient later).
func CalculateILRisk(annualizedVolatility float64, isSmartShielded bool, params types.ScoringParameters) (float64, error) {
	// Validate inputs
	if math.IsNaN(annualizedVolatility) || math.IsInf(annualizedVolatility, 0) {
		return 0, errors.New("annualized volatility is not finite")
	}
	if annualizedVolatility < 0 {
		return 0, errors.New("annualized volatility cannot be negative")
	}
	if math.IsNaN(params.IlHoldingPeriodYears) || math.IsInf(params.IlHoldingPeriodYears, 0) {
		return 0, errors.New("IlHoldingPeriodYears is not finite")
	}
	if math.IsNaN(params.IlConfidenceFactor) || math.IsInf(params.IlConfidenceFactor, 0) {
		return 0, errors.New("IlConfidenceFactor is not finite")
	}
	if math.IsNaN(params.SmartShieldReductionFactor) || math.IsInf(params.SmartShieldReductionFactor, 0) {
		return 0, errors.New("SmartShieldReductionFactor is not finite")
	}

	// Zero volatility means zero IL risk
	if annualizedVolatility == 0 {
		scoreLogger.Debug().
			Float64("annualizedVolatility", annualizedVolatility).
			Msg("Annualized volatility is zero, IL risk is 0")
		return 0, nil
	}

	// Calculate variance (volatility^2)
	variance := math.Pow(annualizedVolatility, 2)
	if math.IsNaN(variance) || math.IsInf(variance, 0) {
		return 0, errors.New("variance calculation resulted in non-finite value")
	}

	// Scale by holding period
	timeScaledVariance := variance * params.IlHoldingPeriodYears
	if math.IsNaN(timeScaledVariance) || math.IsInf(timeScaledVariance, 0) {
		return 0, errors.New("time-scaled variance calculation resulted in non-finite value")
	}

	// Apply confidence factor
	ilRiskBase := params.IlConfidenceFactor * timeScaledVariance
	if math.IsNaN(ilRiskBase) || math.IsInf(ilRiskBase, 0) {
		return 0, errors.New("base IL risk calculation resulted in non-finite value")
	}

	// Apply smart shield reduction if applicable
	ilRiskFinal := ilRiskBase
	if isSmartShielded && params.SmartShieldReductionFactor > 0 {
		reductionMultiplier := 1.0 - params.SmartShieldReductionFactor
		if reductionMultiplier < 0 {
			return 0, errors.New("smart shield reduction factor results in negative multiplier")
		}
		ilRiskFinal = ilRiskBase * reductionMultiplier
		if math.IsNaN(ilRiskFinal) || math.IsInf(ilRiskFinal, 0) {
			return 0, errors.New("smart shield reduction calculation resulted in non-finite value")
		}
		scoreLogger.Debug().
			Float64("baseIlRisk", ilRiskBase).
			Bool("isSmartShielded", isSmartShielded).
			Float64("reductionFactor", params.SmartShieldReductionFactor).
			Float64("finalIlRisk", ilRiskFinal).
			Msg("Smart shield reduction applied to IL risk")
	} else {
		scoreLogger.Debug().
			Float64("baseIlRisk", ilRiskBase).
			Bool("isSmartShielded", isSmartShielded).
			Float64("reductionFactor", params.SmartShieldReductionFactor).
			Msg("No smart shield reduction applied or reduction factor is zero")
	}

	scoreLogger.Debug().
		Float64("inputAnnualizedVolatility", annualizedVolatility).
		Float64("variance", variance).
		Float64("holdingPeriodYears", params.IlHoldingPeriodYears).
		Float64("timeScaledVariance", timeScaledVariance).
		Float64("confidenceFactor", params.IlConfidenceFactor).
		Float64("ilRiskBeforeShield", ilRiskBase).
		Bool("isSmartShielded", isSmartShielded).
		Float64("smartShieldReductionFactor", params.SmartShieldReductionFactor).
		Float64("finalCalculatedILRisk", ilRiskFinal).
		Msg("IL Risk calculated with components")

	return ilRiskFinal, nil
}

// CalculateAgePenalty computes a penalty for newer pools based on their age.
// The penalty decreases linearly until the pool reaches maturity.
// Inputs:
//   - pool: The pool data containing AgeInDays information.
//   - params: The scoring parameters containing age-related thresholds and coefficients.
//
// Output:
//   - The calculated age penalty (negative or zero, as it's a penalty subtracted from the score).
func CalculateAgePenalty(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate parameters
	if math.IsNaN(params.NewPoolCoefficient) || math.IsInf(params.NewPoolCoefficient, 0) {
		return 0, errors.New("NewPoolCoefficient is not finite")
	}

	// Strict validation - no assumptions
	if params.PoolMaturityDays < 0 {
		return 0, errors.New("PoolMaturityDays cannot be negative")
	}
	if params.PoolMaturityDays == 0 {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Str("tokenA", pool.TokenA.Symbol).
			Str("tokenB", pool.TokenB.Symbol).
			Msg("PoolMaturityDays is zero, no age penalty applied")
		return 0.0, nil
	}

	// Age must be non-negative
	if pool.AgeInDays < 0 {
		return 0, errors.New("pool age cannot be negative")
	}

	// If the pool is mature (age >= maturity threshold), no penalty
	if pool.AgeInDays >= params.PoolMaturityDays {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Str("tokenA", pool.TokenA.Symbol).
			Str("tokenB", pool.TokenB.Symbol).
			Int("currentPoolAgeDays", pool.AgeInDays).
			Int("maturityThresholdDays", params.PoolMaturityDays).
			Msg("Pool is mature, no age penalty applied")
		return 0.0, nil
	}

	// Calculate maturity scale
	maturityScale := float64(pool.AgeInDays) / float64(params.PoolMaturityDays)
	if math.IsNaN(maturityScale) || math.IsInf(maturityScale, 0) {
		return 0, errors.New("maturity scale calculation resulted in non-finite value")
	}

	penaltyScale := 1.0 - maturityScale
	if math.IsNaN(penaltyScale) || math.IsInf(penaltyScale, 0) {
		return 0, errors.New("penalty scale calculation resulted in non-finite value")
	}

	agePenalty := params.NewPoolCoefficient * penaltyScale
	if math.IsNaN(agePenalty) || math.IsInf(agePenalty, 0) {
		return 0, errors.New("age penalty calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Int("currentPoolAgeDays", pool.AgeInDays).
		Int("maturityThresholdDays", params.PoolMaturityDays).
		Float64("maturityCompletionScale", maturityScale).
		Float64("penaltyApplicationScale", penaltyScale).
		Float64("newPoolCoefficient", params.NewPoolCoefficient).
		Float64("finalAgePenalty", agePenalty).
		Msg("Age penalty calculated with components")

	return agePenalty, nil
}

// CalculateSentimentAdjustment computes an adjustment to the risk score based on external sentiment data.
// Inputs:
//   - pool: The pool data containing SentimentScore information.
//   - params: The scoring parameters containing SentimentImpactFactor.
//
// Output:
//   - The calculated sentiment adjustment (typically between -1 and 1, scaled by the impact factor).
func CalculateSentimentAdjustment(pool types.Pool, params types.ScoringParameters) (float64, error) {
	// Validate inputs
	if math.IsNaN(pool.SentimentScore) || math.IsInf(pool.SentimentScore, 0) {
		return 0, errors.New("sentiment score is not finite")
	}
	if math.IsNaN(params.SentimentImpactFactor) || math.IsInf(params.SentimentImpactFactor, 0) {
		return 0, errors.New("sentiment impact factor is not finite")
	}

	// If sentiment score is 0 or impact factor is 0, no adjustment needed
	if pool.SentimentScore == 0 || params.SentimentImpactFactor == 0 {
		scoreLogger.Debug().
			Uint64("poolID", uint64(pool.ID)).
			Str("tokenA", pool.TokenA.Symbol).
			Str("tokenB", pool.TokenB.Symbol).
			Float64("rawSentimentScore", pool.SentimentScore).
			Float64("sentimentImpactFactor", params.SentimentImpactFactor).
			Msg("Sentiment adjustment is zero due to zero score or zero factor")
		return 0.0, nil
	}

	sentimentAdjustment := pool.SentimentScore * params.SentimentImpactFactor
	if math.IsNaN(sentimentAdjustment) || math.IsInf(sentimentAdjustment, 0) {
		return 0, errors.New("sentiment adjustment calculation resulted in non-finite value")
	}

	scoreLogger.Debug().
		Uint64("poolID", uint64(pool.ID)).
		Str("tokenA", pool.TokenA.Symbol).
		Str("tokenB", pool.TokenB.Symbol).
		Float64("rawSentimentScore", pool.SentimentScore).
		Float64("sentimentImpactFactor", params.SentimentImpactFactor).
		Float64("finalSentimentAdjustment", sentimentAdjustment).
		Msg("Sentiment adjustment calculated")

	return sentimentAdjustment, nil
}

// ValidatePoolData performs comprehensive validation of pool data to ensure all required fields are populated
func ValidatePoolData(pool types.Pool) error {
	// Validate basic pool identifiers
	if pool.ID == 0 {
		return errors.New("pool ID cannot be zero")
	}

	// Validate token information
	if pool.TokenA.Symbol == "" {
		return errors.New("TokenA symbol cannot be empty")
	}
	if pool.TokenB.Symbol == "" {
		return errors.New("TokenB symbol cannot be empty")
	}

	// Validate financial data - must be non-negative
	if pool.TvlUSD < 0 {
		return errors.New("TVL cannot be negative")
	}
	if pool.Volume7dUSD < 0 {
		return errors.New("7-day volume cannot be negative")
	}

	// Validate APR components - can be negative but must be finite
	if math.IsNaN(pool.EdenRewardsAPR) || math.IsInf(pool.EdenRewardsAPR, 0) {
		return errors.New("eden rewards apr must be finite")
	}
	if math.IsNaN(pool.UsdcFeesAPR) || math.IsInf(pool.UsdcFeesAPR, 0) {
		return errors.New("usdc fees apr must be finite")
	}
	if math.IsNaN(pool.PriceImpactAPR) || math.IsInf(pool.PriceImpactAPR, 0) {
		return errors.New("price impact apr must be finite")
	}

	// Validate volatility data
	if pool.TokenA.Volatility < 0 {
		return errors.New("TokenA volatility cannot be negative")
	}
	if math.IsNaN(pool.TokenA.Volatility) || math.IsInf(pool.TokenA.Volatility, 0) {
		return errors.New("TokenA volatility must be finite")
	}

	// Validate pool age
	if pool.AgeInDays < 0 {
		return errors.New("pool age cannot be negative")
	}

	// Validate weights (should sum to approximately 1.0)
	totalWeight := pool.WeightA + pool.WeightB
	if totalWeight <= 0 || math.Abs(totalWeight-1.0) > 0.01 {
		return errors.New("pool weights must be positive and sum to approximately 1.0")
	}

	// Validate sentiment score if provided (should be between -1 and 1)
	if pool.SentimentScore != 0 && (pool.SentimentScore < -1.0 || pool.SentimentScore > 1.0) {
		return errors.New("sentiment score must be between -1.0 and 1.0")
	}

	// Validate vault position state if position exists
	if pool.HasCurrentPosition && pool.CurrentPositionAgeDays < 0 {
		return errors.New("current position age cannot be negative when position exists")
	}

	return nil
}

// ValidateScoringParameters performs validation of scoring parameters to ensure they are reasonable
func ValidateScoringParameters(params types.ScoringParameters) error {
	// Validate APR weights (should be non-negative and sum to something reasonable)
	if params.EdenWeight < 0 || params.UsdcFeeWeight < 0 || params.PriceImpactWeight < 0 {
		return errors.New("APR weights cannot be negative")
	}

	totalAprWeight := params.EdenWeight + params.UsdcFeeWeight + params.PriceImpactWeight
	if totalAprWeight <= 0 {
		return errors.New("total APR weights must be positive")
	}

	// Validate coefficient values are finite
	coefficients := []struct {
		value float64
		name  string
	}{
		{params.AprCoefficient, "AprCoefficient"},
		{params.TradingVolumeCoefficient, "TradingVolumeCoefficient"},
		{params.IlRiskCoefficient, "IlRiskCoefficient"},
		{params.VolatilityCoefficient, "VolatilityCoefficient"},
		{params.TvlCoefficient, "TvlCoefficient"},
		{params.NewPoolCoefficient, "NewPoolCoefficient"},
		{params.ContinuityCoefficient, "ContinuityCoefficient"},
		{params.SmartShieldBonus, "SmartShieldBonus"},
		{params.SentimentImpactFactor, "SentimentImpactFactor"},
	}

	for _, coeff := range coefficients {
		if math.IsNaN(coeff.value) || math.IsInf(coeff.value, 0) {
			return errors.New(coeff.name + " must be finite")
		}
	}

	// Validate threshold values
	if params.MinTVLThreshold < 0 {
		return errors.New("MinTVLThreshold cannot be negative")
	}
	if params.PoolMaturityDays < 0 {
		return errors.New("PoolMaturityDays cannot be negative")
	}
	if params.ContinuityLookbackDays < 0 {
		return errors.New("ContinuityLookbackDays cannot be negative")
	}

	// Validate IL risk parameters
	if params.IlHoldingPeriodYears <= 0 {
		return errors.New("IlHoldingPeriodYears must be positive")
	}
	if params.IlConfidenceFactor <= 0 {
		return errors.New("IlConfidenceFactor must be positive")
	}
	if params.SmartShieldReductionFactor < 0 || params.SmartShieldReductionFactor > 1 {
		return errors.New("SmartShieldReductionFactor must be between 0 and 1")
	}

	return nil
}

// CalculatePoolScores calculates scores for multiple pools and returns an array of results.
// This function processes each pool individually using the existing CalculatePoolScore function.
// It ensures all pools are processed with the same strict validation standards.
// Inputs:
//   - pools: Array of pool data structures to be scored
//   - params: The scoring parameters defining weights, coefficients, and thresholds
//
// Output:
//   - Array of PoolScoreResult containing scores and component breakdowns for each pool
//   - An error if any pool fails validation or scoring
func CalculatePoolScores(pools []types.Pool, params types.ScoringParameters) ([]types.PoolScoreResult, error) {
	if len(pools) == 0 {
		scoreLogger.Error().Msg("No pools provided for scoring")
		return nil, errors.New("no pools provided for scoring")
	}

	// Validate scoring parameters once for all pools
	if err := ValidateScoringParameters(params); err != nil {
		scoreLogger.Error().
			Err(err).
			Msg("Scoring parameters validation failed")
		return nil, errors.Join(ErrInvalidScoringParameters, err)
	}

	scoreLogger.Info().
		Int("poolCount", len(pools)).
		Msg("Starting batch pool scoring")

	results := make([]types.PoolScoreResult, 0, len(pools))

	for i, pool := range pools {
		scoreLogger.Debug().
			Int("poolIndex", i).
			Uint64("poolID", uint64(pool.ID)).
			Str("tokenA", pool.TokenA.Symbol).
			Str("tokenB", pool.TokenB.Symbol).
			Msg("Processing pool in batch")

		// Calculate score for individual pool using existing function
		result, err := CalculatePoolScore(pool, params)
		if err != nil {
			scoreLogger.Error().
				Err(err).
				Int("poolIndex", i).
				Uint64("poolID", uint64(pool.ID)).
				Msg("Pool scoring failed in batch processing")
			return nil, fmt.Errorf("pool %d scoring failed: %w", pool.ID, err)
		}

		results = append(results, result)

		scoreLogger.Debug().
			Int("poolIndex", i).
			Uint64("poolID", uint64(pool.ID)).
			Float64("score", result.Score).
			Msg("Pool scored successfully in batch")
	}

	scoreLogger.Info().
		Int("poolCount", len(pools)).
		Int("successfullyScored", len(results)).
		Msg("Batch pool scoring completed")

	return results, nil
}

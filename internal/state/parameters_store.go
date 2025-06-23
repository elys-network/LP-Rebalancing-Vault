// ./internal/state/parameters_store.go
package state

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/elys-network/avm/internal/types"
	"github.com/rs/zerolog/log"
)

// SaveScoringParameters saves a new version of scoring parameters.
func SaveScoringParameters(params types.ScoringParameters, configName string, version int, makeActive bool) (int64, error) {
	if DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	tx, err := DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p) // Re-panic after rollback
		} else if err != nil {
			tx.Rollback() // Rollback if error occurred
		}
	}()

	if makeActive {
		stmtDeactivate := `UPDATE scoring_parameters SET is_active = FALSE WHERE config_name = $1 AND is_active = TRUE;`
		_, err = tx.Exec(stmtDeactivate, configName)
		if err != nil {
			return 0, fmt.Errorf("failed to deactivate existing active parameters for %s: %w", configName, err)
		}
	}

	// Adjusted to match the schema in db.go (which was missing the detailed slippage params and volatility lookback)
	// The number of placeholders must match the number of values.
	// Original schema had 33 value columns (params_id is serial, created_at defaults)
	// Current types.ScoringParameters implies fewer based on the schema in db.go
	// Let's count based on the schema in your db.go:
	// version, config_name, is_active, activated_at, created_at (5)
	// eden_w, usdc_fee_w, price_impact_w (3)
	// apr_c, trading_vol_c (2)
	// il_risk_c, volatility_c, new_pool_c, tvl_c (4)
	// smart_shield_b, continuity_c, sentiment_impact_f (3)
	// il_conf_f, il_hold_p_y, smart_shield_reduct_f (3)
	// min_tvl_t, pool_mat_d, cont_look_d (3)
	// rebal_thresh_a, max_pools, min_alloc, max_alloc (4)
	// opt_int_cycles (1)
	// TOTAL = 5 + 3 + 2 + 4 + 3 + 3 + 3 + 4 + 1 = 28 values to insert (excluding params_id, created_at if it has default)
	// The schema in db.go has `created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP`
	// and `activated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP`
	// So we only need to provide activated_at if we want to override the default.
	// Let's provide both activated_at and created_at for explicitness.

	stmt := `
        INSERT INTO scoring_parameters (
            version, config_name, is_active, activated_at, created_at,
            eden_weight, usdc_fee_weight, price_impact_weight,
            apr_coefficient, trading_volume_coefficient,
            il_risk_coefficient, volatility_coefficient, new_pool_coefficient, tvl_coefficient,
            smart_shield_bonus, continuity_coefficient, sentiment_impact_factor,
            il_confidence_factor, il_holding_period_years, smart_shield_reduction_factor,
            min_tvl_threshold, pool_maturity_days, continuity_lookback_days,
            rebalance_threshold_amount, max_rebalance_percent_per_cycle, max_pools, min_allocation, max_allocation,
            smart_shield_slippage_percent, normal_pool_slippage_percent, min_liquid_usdc_buffer, learning_rate, max_parameter_change,
            optimization_interval_cycles 
        ) VALUES (
            $1, $2, $3, $4, $5,  -- version, config_name, is_active, activated_at, created_at
            $6, $7, $8,          -- eden_w, usdc_fee_w, price_impact_w
            $9, $10,             -- apr_c, trading_vol_c
            $11, $12, $13, $14,  -- il_risk_c, volatility_c, new_pool_c, tvl_c
            $15, $16, $17,       -- smart_shield_b, continuity_c, sentiment_impact_f
            $18, $19, $20,       -- il_conf_f, il_hold_p_y, smart_shield_reduct_f
            $21, $22, $23,       -- min_tvl_t, pool_mat_d, cont_look_d
            $24, $25, $26, $27, $28,  -- rebal_thresh_a, max_rebalance_percent_per_cycle, max_pools, min_alloc, max_alloc
            $29, $30, $31, $32, $33,  -- smart_shield_slippage_percent, normal_pool_slippage_percent, min_liquid_usdc_buffer, learning_rate, max_parameter_change
            $34                  -- opt_int_cycles
        ) RETURNING params_id;`

	var paramsID int64
	currentTime := time.Now()
	err = tx.QueryRow(
		stmt,
		version, configName, makeActive, currentTime, currentTime, // activated_at, created_at
		params.EdenWeight, params.UsdcFeeWeight, params.PriceImpactWeight,
		params.AprCoefficient, params.TradingVolumeCoefficient,
		params.IlRiskCoefficient, params.VolatilityCoefficient, params.NewPoolCoefficient, params.TvlCoefficient,
		params.SmartShieldBonus, params.ContinuityCoefficient, params.SentimentImpactFactor,
		params.IlConfidenceFactor, params.IlHoldingPeriodYears, params.SmartShieldReductionFactor,
		params.MinTVLThreshold, params.PoolMaturityDays, params.ContinuityLookbackDays,
		params.RebalanceThresholdAmount, params.MaxRebalancePercentPerCycle, params.MaxPools, params.MinAllocation, params.MaxAllocation,
		params.SmartShieldSlippagePercent, params.NormalPoolSlippagePercent, params.MinLiquidUSDCBuffer, params.LearningRate, params.MaxParameterChange,
		params.OptimizationIntervalCycles,
	).Scan(&paramsID)

	if err != nil {
		return 0, fmt.Errorf("failed to insert scoring parameters: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Info().
		Int("version", version).
		Str("config", configName).
		Int64("params_id", paramsID).
		Bool("active", makeActive).
		Msg("Saved scoring parameters")
	return paramsID, nil
}

// LoadActiveScoringParameters loads the currently active scoring parameters.
func LoadActiveScoringParameters(configName string) (*types.ScoringParameters, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	query := `
        SELECT
            eden_weight, usdc_fee_weight, price_impact_weight,
            apr_coefficient, trading_volume_coefficient,
            il_risk_coefficient, volatility_coefficient, new_pool_coefficient, tvl_coefficient,
            smart_shield_bonus, continuity_coefficient, sentiment_impact_factor,
            il_confidence_factor, il_holding_period_years, smart_shield_reduction_factor,
            min_tvl_threshold, pool_maturity_days, continuity_lookback_days,
            rebalance_threshold_amount, max_rebalance_percent_per_cycle, max_pools, min_allocation, max_allocation,
            smart_shield_slippage_percent, normal_pool_slippage_percent, min_liquid_usdc_buffer, learning_rate, max_parameter_change,
            optimization_interval_cycles
        FROM scoring_parameters
        WHERE config_name = $1 AND is_active = TRUE
        ORDER BY activated_at DESC
        LIMIT 1;`

	p := &types.ScoringParameters{}
	row := DB.QueryRow(query, configName)
	err := row.Scan(
		&p.EdenWeight, &p.UsdcFeeWeight, &p.PriceImpactWeight,
		&p.AprCoefficient, &p.TradingVolumeCoefficient,
		&p.IlRiskCoefficient, &p.VolatilityCoefficient, &p.NewPoolCoefficient, &p.TvlCoefficient,
		&p.SmartShieldBonus, &p.ContinuityCoefficient, &p.SentimentImpactFactor,
		&p.IlConfidenceFactor, &p.IlHoldingPeriodYears, &p.SmartShieldReductionFactor,
		&p.MinTVLThreshold, &p.PoolMaturityDays, &p.ContinuityLookbackDays,
		&p.RebalanceThresholdAmount, &p.MaxRebalancePercentPerCycle, &p.MaxPools, &p.MinAllocation, &p.MaxAllocation,
		&p.SmartShieldSlippagePercent, &p.NormalPoolSlippagePercent, &p.MinLiquidUSDCBuffer, &p.LearningRate, &p.MaxParameterChange,
		&p.OptimizationIntervalCycles,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no active scoring parameters found for config '%s'", configName)
		}
		return nil, fmt.Errorf("failed to scan active scoring parameters for config '%s': %w", configName, err)
	}
	log.Info().Str("config", configName).Msg("Loaded active scoring parameters")
	return p, nil
}

// LoadLatestScoringParameters loads the most recently activated scoring parameters for a given config name.
func LoadLatestScoringParameters(configName string) (*types.ScoringParameters, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	query := `
        SELECT
            eden_weight, usdc_fee_weight, price_impact_weight,
            apr_coefficient, trading_volume_coefficient,
            il_risk_coefficient, volatility_coefficient, new_pool_coefficient, tvl_coefficient,
            smart_shield_bonus, continuity_coefficient, sentiment_impact_factor,
            il_confidence_factor, il_holding_period_years, smart_shield_reduction_factor,
            min_tvl_threshold, pool_maturity_days, continuity_lookback_days,
            rebalance_threshold_amount, max_rebalance_percent_per_cycle, max_pools, min_allocation, max_allocation,
            smart_shield_slippage_percent, normal_pool_slippage_percent, min_liquid_usdc_buffer, learning_rate, max_parameter_change,
            optimization_interval_cycles
        FROM scoring_parameters
        WHERE config_name = $1
        ORDER BY activated_at DESC, created_at DESC
        LIMIT 1;`

	p := &types.ScoringParameters{}
	row := DB.QueryRow(query, configName)
	err := row.Scan(
		&p.EdenWeight, &p.UsdcFeeWeight, &p.PriceImpactWeight,
		&p.AprCoefficient, &p.TradingVolumeCoefficient,
		&p.IlRiskCoefficient, &p.VolatilityCoefficient, &p.NewPoolCoefficient, &p.TvlCoefficient,
		&p.SmartShieldBonus, &p.ContinuityCoefficient, &p.SentimentImpactFactor,
		&p.IlConfidenceFactor, &p.IlHoldingPeriodYears, &p.SmartShieldReductionFactor,
		&p.MinTVLThreshold, &p.PoolMaturityDays, &p.ContinuityLookbackDays,
		&p.RebalanceThresholdAmount, &p.MaxRebalancePercentPerCycle, &p.MaxPools, &p.MinAllocation, &p.MaxAllocation,
		&p.SmartShieldSlippagePercent, &p.NormalPoolSlippagePercent, &p.MinLiquidUSDCBuffer, &p.LearningRate, &p.MaxParameterChange,
		&p.OptimizationIntervalCycles,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no scoring parameters found for config '%s'", configName)
		}
		return nil, fmt.Errorf("failed to scan latest scoring parameters for config '%s': %w", configName, err)
	}
	log.Info().Str("config", configName).Msg("Loaded latest scoring parameters (by activation/creation time)")
	return p, nil
}

// GetActiveScoringParametersID returns the params_id of the currently active scoring parameters
func GetActiveScoringParametersID(configName string) (*int64, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	query := `
        SELECT params_id
        FROM scoring_parameters
        WHERE config_name = $1 AND is_active = TRUE
        ORDER BY activated_at DESC
        LIMIT 1;`

	var paramsID int64
	row := DB.QueryRow(query, configName)
	err := row.Scan(&paramsID)

	if err != nil {
		if err == sql.ErrNoRows {
			// No active parameters found - this is valid, return nil
			log.Debug().Str("config", configName).Msg("No active scoring parameters found")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get active scoring parameters ID for config '%s': %w", configName, err)
	}

	log.Debug().
		Str("config", configName).
		Int64("params_id", paramsID).
		Msg("Retrieved active scoring parameters ID")
	
	return &paramsID, nil
}

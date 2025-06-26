// ./internal/state/db.go
package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
	"github.com/rs/zerolog/log"
)

// DB is a global database connection pool.
var DB *sql.DB

// DBConfig holds database connection parameters.
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string // "disable", "require", "verify-full", etc.
}

// InitDB initializes the database connection pool.
func InitDB(cfg DBConfig) error {
	psqlInfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

	var err error
	DB, err = sql.Open("postgres", psqlInfo)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}

	DB.SetMaxOpenConns(25)
	DB.SetMaxIdleConns(25)
	DB.SetConnMaxLifetime(5 * time.Minute)

	err = DB.Ping()
	if err != nil {
		DB.Close()
		return fmt.Errorf("failed to ping database: %w", err)
	}

	log.Info().Msg("Successfully connected to the PostgreSQL database!")
	return nil
}

// CloseDB closes the database connection pool.
func CloseDB() {
	if DB != nil {
		log.Info().Msg("Closing database connection...")
		if err := DB.Close(); err != nil {
			log.Error().Err(err).Msg("Error closing database connection")
		}
	}
}

// EnsureSchema applies the necessary DDL to create tables if they don't exist.
func EnsureSchema() error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}

	// Drop old, less comprehensive snapshot tables to replace them with the new one.
	// This is safe to run multiple times.
	dropOldTablesSQL := `
		DROP TABLE IF EXISTS investment_snapshots CASCADE;
		DROP TABLE IF EXISTS performance_snapshots CASCADE;
	`
	if _, err := DB.Exec(dropOldTablesSQL); err != nil {
		return fmt.Errorf("failed to drop old snapshot tables: %w", err)
	}

	schemaSQL := `
		CREATE TABLE IF NOT EXISTS scoring_parameters (
			params_id SERIAL PRIMARY KEY,
			version INTEGER NOT NULL DEFAULT 1,
			config_name VARCHAR(255) NOT NULL DEFAULT 'default',
			is_active BOOLEAN NOT NULL DEFAULT FALSE,
			activated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			eden_weight DECIMAL(10, 4) NOT NULL, usdc_fee_weight DECIMAL(10, 4) NOT NULL, price_impact_weight DECIMAL(10, 4) NOT NULL,
			apr_coefficient DECIMAL(10, 4) NOT NULL, trading_volume_coefficient DECIMAL(10, 4) NOT NULL,
			il_risk_coefficient DECIMAL(10, 4) NOT NULL, volatility_coefficient DECIMAL(10, 4) NOT NULL,
			new_pool_coefficient DECIMAL(10, 4) NOT NULL, tvl_coefficient DECIMAL(10, 4) NOT NULL,
			smart_shield_bonus DECIMAL(10, 4) NOT NULL, continuity_coefficient DECIMAL(10, 4) NOT NULL,
			sentiment_impact_factor DECIMAL(10, 4) NOT NULL,
			il_confidence_factor DECIMAL(10, 4) NOT NULL, il_holding_period_years DECIMAL(10, 8) NOT NULL,
			smart_shield_reduction_factor DECIMAL(10, 4) NOT NULL,
			min_tvl_threshold DECIMAL(20, 8) NOT NULL, pool_maturity_days INTEGER NOT NULL, continuity_lookback_days INTEGER NOT NULL,
			rebalance_threshold_amount DECIMAL(20, 8) NOT NULL, max_pools INTEGER NOT NULL,
			min_allocation DECIMAL(10, 8) NOT NULL, max_allocation DECIMAL(10, 8) NOT NULL,
			smart_shield_slippage_percent DECIMAL(10, 8) NOT NULL,
			normal_pool_slippage_percent DECIMAL(10, 8) NOT NULL,
			min_liquid_usdc_buffer DECIMAL(20, 8) NOT NULL,
			learning_rate DECIMAL(10, 8) NOT NULL,
			max_parameter_change DECIMAL(10, 8) NOT NULL,
			optimization_interval_cycles INTEGER NOT NULL,
			elys_forced_allocation_minimum DECIMAL(10, 8) NOT NULL DEFAULT 0.10,
			CONSTRAINT uq_scoring_parameters_config_version UNIQUE (config_name, version)
		);
		CREATE INDEX IF NOT EXISTS idx_scoring_parameters_config_active_timestamp ON scoring_parameters(config_name, is_active, activated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_scoring_parameters_config_timestamp ON scoring_parameters(config_name, activated_at DESC);

		-- Migration: Add new slippage columns if they don't exist
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS smart_shield_slippage_percent DECIMAL(10, 8) DEFAULT 1.0;
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS normal_pool_slippage_percent DECIMAL(10, 8) DEFAULT 3.0;
		
		-- Migration: Remove old column if it exists (but keep for backwards compatibility temporarily)
		-- We'll populate new columns from old one if they're null
		DO $$
		BEGIN
			-- If old column exists and new columns are null, migrate the data
			IF EXISTS (SELECT column_name FROM information_schema.columns 
					   WHERE table_name='scoring_parameters' AND column_name='max_swap_price_impact_percent') THEN
				-- Migrate data: use old value for SmartShield, 3x for normal pools
				UPDATE scoring_parameters 
				SET smart_shield_slippage_percent = max_swap_price_impact_percent,
					normal_pool_slippage_percent = GREATEST(max_swap_price_impact_percent * 3, 3.0)
				WHERE smart_shield_slippage_percent IS NULL OR normal_pool_slippage_percent IS NULL;
			END IF;
		END
		$$;

		-- Set NOT NULL constraints after migration
		ALTER TABLE scoring_parameters ALTER COLUMN smart_shield_slippage_percent SET NOT NULL;
		ALTER TABLE scoring_parameters ALTER COLUMN normal_pool_slippage_percent SET NOT NULL;

		-- Add missing columns to existing scoring_parameters table if they don't exist
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS min_liquid_usdc_buffer DECIMAL(20, 8) DEFAULT 50.0;
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS max_rebalance_percent_per_cycle DECIMAL(10, 8) DEFAULT 5.0;
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS learning_rate DECIMAL(10, 8) DEFAULT 0.01;
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS max_parameter_change DECIMAL(10, 8) DEFAULT 0.1;
		ALTER TABLE scoring_parameters ADD COLUMN IF NOT EXISTS elys_forced_allocation_minimum DECIMAL(10, 8) DEFAULT 0.10;
		-- Update the columns to NOT NULL after adding defaults
		ALTER TABLE scoring_parameters ALTER COLUMN min_liquid_usdc_buffer SET NOT NULL;
		ALTER TABLE scoring_parameters ALTER COLUMN max_rebalance_percent_per_cycle SET NOT NULL;
		ALTER TABLE scoring_parameters ALTER COLUMN learning_rate SET NOT NULL;
		ALTER TABLE scoring_parameters ALTER COLUMN max_parameter_change SET NOT NULL;
		ALTER TABLE scoring_parameters ALTER COLUMN elys_forced_allocation_minimum SET NOT NULL;

		CREATE TABLE IF NOT EXISTS action_receipts (
			receipt_id SERIAL PRIMARY KEY,
			action_timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			action_type VARCHAR(50) NOT NULL,
			pool_id BIGINT NOT NULL,
			requested_amount_usd DECIMAL(20, 8),
			actual_amount_usd DECIMAL(20, 8),
			success BOOLEAN NOT NULL,
			message TEXT,
			tokens_deposited JSONB,
			tokens_withdrawn JSONB
		);
		CREATE INDEX IF NOT EXISTS idx_action_receipts_timestamp ON action_receipts(action_timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_action_receipts_pool_id ON action_receipts(pool_id);
		CREATE INDEX IF NOT EXISTS idx_action_receipts_action_type ON action_receipts(action_type);

		-- NEW COMPREHENSIVE SNAPSHOT TABLE
		CREATE TABLE IF NOT EXISTS cycle_snapshots (
			snapshot_id SERIAL PRIMARY KEY,
			cycle_number INTEGER NOT NULL,
			snapshot_timestamp TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			scoring_params_id INTEGER REFERENCES scoring_parameters(params_id),

			-- Pre-Action State
			initial_vault_value_usd DECIMAL(20, 8) NOT NULL,
			initial_liquid_usdc DECIMAL(20, 8) NOT NULL,
			initial_positions JSONB,

			-- The Plan
			target_allocations JSONB,
			action_plan JSONB,

			-- The Outcome
			final_vault_value_usd DECIMAL(20, 8) NOT NULL,
			final_liquid_usdc DECIMAL(20, 8) NOT NULL,
			final_positions JSONB,
			transaction_hashes TEXT[], -- PostgreSQL array of strings for tx hashes
			action_receipts JSONB,

			-- Performance Metrics
			allocation_efficiency_percent DECIMAL(10, 4),
			net_return_usd DECIMAL(20, 8),
			total_slippage_usd DECIMAL(20, 8),
			total_gas_fee_usd DECIMAL(20, 8)
		);
		CREATE INDEX IF NOT EXISTS idx_cycle_snapshots_timestamp ON cycle_snapshots(snapshot_timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_cycle_snapshots_cycle ON cycle_snapshots(cycle_number DESC);

		-- Cycle counter table for persistent global cycle tracking
		CREATE TABLE IF NOT EXISTS cycle_counter (
			id INTEGER PRIMARY KEY DEFAULT 1,
			current_cycle INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CONSTRAINT single_row_check CHECK (id = 1)
		);

		-- Insert initial row if it doesn't exist
		INSERT INTO cycle_counter (id, current_cycle) 
		VALUES (1, 0) 
		ON CONFLICT (id) DO NOTHING;
	`
	_, err := DB.Exec(schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to execute schema DDL: %w", err)
	}
	log.Info().Msg("Database schema ensured (including new cycle_snapshots table).")
	return nil
}

// TestDBConnection tests if the database connection is healthy
func TestDBConnection() error {
	if DB == nil {
		return fmt.Errorf("database connection is nil")
	}
	
	// Use a short timeout context for health checks
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	err := DB.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	
	return nil
}

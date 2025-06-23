package state

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/elys-network/avm/internal/types" // PostgreSQL driver for array support
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

// VaultSummary represents high-level vault statistics
type VaultSummary struct {
	TotalValue    float64 `json:"total_value"`
	LiquidUSDC    float64 `json:"liquid_usdc"`
	PositionCount int     `json:"position_count"`
	TotalCycles   int     `json:"total_cycles"`
	LastUpdated   string  `json:"last_updated"`
}

// PerformanceMetrics represents aggregated performance data
type PerformanceMetrics struct {
	TotalReturn             float64 `json:"total_return"`
	TotalGasFees            float64 `json:"total_gas_fees"`
	TotalSlippage           float64 `json:"total_slippage"`
	AvgAllocationEfficiency float64 `json:"avg_allocation_efficiency"`
	TotalCycles             int     `json:"total_cycles"`
	SuccessfulCycles        int     `json:"successful_cycles"`
}

// GetRecentCycles retrieves recent cycle snapshots with pagination
func GetRecentCycles(limit int) ([]types.CycleSnapshot, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if limit <= 0 || limit > 100 {
		limit = 10 // Default limit
	}

	query := `
		SELECT 
			snapshot_id, cycle_number, snapshot_timestamp, scoring_params_id,
			initial_vault_value_usd, initial_liquid_usdc, initial_positions,
			target_allocations, action_plan,
			final_vault_value_usd, final_liquid_usdc, final_positions,
			transaction_hashes, action_receipts,
			allocation_efficiency_percent, net_return_usd, total_slippage_usd, total_gas_fee_usd
		FROM cycle_snapshots 
		ORDER BY snapshot_timestamp DESC 
		LIMIT $1
	`

	rows, err := DB.Query(query, limit)
	if err != nil {
		log.Error().Err(err).Msg("Failed to query recent cycles")
		return nil, fmt.Errorf("failed to query recent cycles: %w", err)
	}
	defer rows.Close()

	var cycles []types.CycleSnapshot
	for rows.Next() {
		var cycle types.CycleSnapshot
		var initialPositionsJSON, targetAllocationsJSON, actionPlanJSON, finalPositionsJSON, actionReceiptsJSON []byte

		err := rows.Scan(
			&cycle.SnapshotID, &cycle.CycleNumber, &cycle.Timestamp, &cycle.ScoringParamsID,
			&cycle.InitialVaultValueUSD, &cycle.InitialLiquidUSDC, &initialPositionsJSON,
			&targetAllocationsJSON, &actionPlanJSON,
			&cycle.FinalVaultValueUSD, &cycle.FinalLiquidUSDC, &finalPositionsJSON,
			pq.Array(&cycle.TransactionHashes), &actionReceiptsJSON, // Use pq.Array for PostgreSQL array
			&cycle.AllocationEfficiencyPercent, &cycle.NetReturnUSD, &cycle.TotalSlippageUSD, &cycle.TotalGasFeeUSD,
		)
		if err != nil {
			log.Error().Err(err).Msg("Failed to scan cycle row")
			continue // Skip this row and continue with others
		}

		// Unmarshal JSON fields
		if err := unmarshalJSONFields(&cycle, initialPositionsJSON, targetAllocationsJSON, actionPlanJSON, finalPositionsJSON, actionReceiptsJSON); err != nil {
			log.Error().Err(err).Int("cycle_number", cycle.CycleNumber).Msg("Failed to unmarshal JSON fields for cycle")
			continue // Skip this row and continue with others
		}

		cycles = append(cycles, cycle)
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("Error occurred during row iteration")
		return nil, fmt.Errorf("error during row iteration: %w", err)
	}

	log.Info().Int("count", len(cycles)).Int("limit", limit).Msg("Retrieved recent cycles")
	return cycles, nil
}

// unmarshalJSONFields unmarshals JSON fields for a cycle snapshot
func unmarshalJSONFields(cycle *types.CycleSnapshot, initialPositionsJSON, targetAllocationsJSON, actionPlanJSON, finalPositionsJSON, actionReceiptsJSON []byte) error {
	// Unmarshal initial positions
	if len(initialPositionsJSON) > 0 {
		if err := json.Unmarshal(initialPositionsJSON, &cycle.InitialPositions); err != nil {
			return fmt.Errorf("failed to unmarshal initial positions: %w", err)
		}
	}

	// Unmarshal target allocations
	if len(targetAllocationsJSON) > 0 {
		if err := json.Unmarshal(targetAllocationsJSON, &cycle.TargetAllocations); err != nil {
			return fmt.Errorf("failed to unmarshal target allocations: %w", err)
		}
	}

	// Unmarshal action plan
	if len(actionPlanJSON) > 0 {
		if err := json.Unmarshal(actionPlanJSON, &cycle.ActionPlan); err != nil {
			return fmt.Errorf("failed to unmarshal action plan: %w", err)
		}
	}

	// Unmarshal final positions
	if len(finalPositionsJSON) > 0 {
		if err := json.Unmarshal(finalPositionsJSON, &cycle.FinalPositions); err != nil {
			return fmt.Errorf("failed to unmarshal final positions: %w", err)
		}
	}

	// Unmarshal action receipts
	if len(actionReceiptsJSON) > 0 {
		if err := json.Unmarshal(actionReceiptsJSON, &cycle.ActionReceipts); err != nil {
			return fmt.Errorf("failed to unmarshal action receipts: %w", err)
		}
	}

	return nil
}

// GetCycleByID retrieves a specific cycle by its ID
func GetCycleByID(snapshotID int64) (*types.CycleSnapshot, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	query := `
		SELECT 
			snapshot_id, cycle_number, snapshot_timestamp, scoring_params_id,
			initial_vault_value_usd, initial_liquid_usdc, initial_positions,
			target_allocations, action_plan,
			final_vault_value_usd, final_liquid_usdc, final_positions,
			transaction_hashes, action_receipts,
			allocation_efficiency_percent, net_return_usd, total_slippage_usd, total_gas_fee_usd
		FROM cycle_snapshots 
		WHERE snapshot_id = $1
	`

	var cycle types.CycleSnapshot
	var initialPositionsJSON, targetAllocationsJSON, actionPlanJSON, finalPositionsJSON, actionReceiptsJSON []byte

	err := DB.QueryRow(query, snapshotID).Scan(
		&cycle.SnapshotID, &cycle.CycleNumber, &cycle.Timestamp, &cycle.ScoringParamsID,
		&cycle.InitialVaultValueUSD, &cycle.InitialLiquidUSDC, &initialPositionsJSON,
		&targetAllocationsJSON, &actionPlanJSON,
		&cycle.FinalVaultValueUSD, &cycle.FinalLiquidUSDC, &finalPositionsJSON,
		pq.Array(&cycle.TransactionHashes), &actionReceiptsJSON, // Use pq.Array for PostgreSQL array
		&cycle.AllocationEfficiencyPercent, &cycle.NetReturnUSD, &cycle.TotalSlippageUSD, &cycle.TotalGasFeeUSD,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("cycle with ID %d not found", snapshotID)
		}
		log.Error().Err(err).Int64("snapshot_id", snapshotID).Msg("Failed to query cycle by ID")
		return nil, fmt.Errorf("failed to query cycle by ID: %w", err)
	}

	// Unmarshal JSON fields
	if err := unmarshalJSONFields(&cycle, initialPositionsJSON, targetAllocationsJSON, actionPlanJSON, finalPositionsJSON, actionReceiptsJSON); err != nil {
		log.Error().Err(err).Int64("snapshot_id", snapshotID).Msg("Failed to unmarshal JSON fields for cycle")
		return nil, fmt.Errorf("failed to unmarshal JSON fields: %w", err)
	}

	log.Info().Int64("snapshot_id", snapshotID).Int("cycle_number", cycle.CycleNumber).Msg("Retrieved cycle by ID")
	return &cycle, nil
}

// GetVaultSummary retrieves high-level vault statistics
func GetVaultSummary() (*VaultSummary, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	summary := &VaultSummary{}

	// Get latest vault value and liquid USDC from most recent cycle
	query := `
		SELECT 
			final_vault_value_usd, 
			final_liquid_usdc,
			snapshot_timestamp
		FROM cycle_snapshots 
		ORDER BY snapshot_timestamp DESC 
		LIMIT 1
	`

	var lastUpdated sql.NullString
	err := DB.QueryRow(query).Scan(&summary.TotalValue, &summary.LiquidUSDC, &lastUpdated)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get latest vault values: %w", err)
	}

	if lastUpdated.Valid {
		summary.LastUpdated = lastUpdated.String
	}

	// Get total cycle count
	err = DB.QueryRow("SELECT COUNT(*) FROM cycle_snapshots").Scan(&summary.TotalCycles)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get total cycle count")
	}

	// Get position count from latest cycle (simplified - would need to parse JSON for exact count)
	// For now, we'll estimate based on non-zero final positions
	summary.PositionCount = 4 // Placeholder - could be calculated from latest cycle's final_positions JSON

	log.Info().Float64("totalValue", summary.TotalValue).Int("totalCycles", summary.TotalCycles).Msg("Retrieved vault summary")
	return summary, nil
}

// GetPerformanceMetrics retrieves aggregated performance metrics
func GetPerformanceMetrics() (*PerformanceMetrics, error) {
	if DB == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	metrics := &PerformanceMetrics{}

	// Get aggregated metrics from all cycles
	query := `
		SELECT 
			COALESCE(SUM(net_return_usd), 0) as total_return,
			COALESCE(SUM(total_gas_fee_usd), 0) as total_gas_fees,
			COALESCE(SUM(total_slippage_usd), 0) as total_slippage,
			COALESCE(AVG(allocation_efficiency_percent), 0) as avg_allocation_efficiency,
			COUNT(*) as total_cycles,
			COUNT(CASE WHEN net_return_usd >= 0 THEN 1 END) as successful_cycles
		FROM cycle_snapshots
	`

	err := DB.QueryRow(query).Scan(
		&metrics.TotalReturn,
		&metrics.TotalGasFees,
		&metrics.TotalSlippage,
		&metrics.AvgAllocationEfficiency,
		&metrics.TotalCycles,
		&metrics.SuccessfulCycles,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get performance metrics: %w", err)
	}

	log.Info().
		Float64("totalReturn", metrics.TotalReturn).
		Float64("totalGasFees", metrics.TotalGasFees).
		Int("totalCycles", metrics.TotalCycles).
		Msg("Retrieved performance metrics")

	return metrics, nil
}

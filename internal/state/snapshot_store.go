// ./internal/state/snapshot_store.go
package state

import (
	"encoding/json"
	"fmt"

	"github.com/elys-network/avm/internal/types"
	"github.com/lib/pq" // PostgreSQL driver for array support
	"github.com/rs/zerolog/log"
)

// SaveCycleSnapshot saves a complete cycle snapshot to the database.
func SaveCycleSnapshot(snapshot types.CycleSnapshot) (int64, error) {
	if DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	// Marshal all JSONB fields
	initialPositionsJSON, err := json.Marshal(snapshot.InitialPositions)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal initial_positions: %w", err)
	}

	finalPositionsJSON, err := json.Marshal(snapshot.FinalPositions)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal final_positions: %w", err)
	}

	targetAllocationsJSON, err := json.Marshal(snapshot.TargetAllocations)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal target_allocations: %w", err)
	}

	actionPlanJSON, err := json.Marshal(snapshot.ActionPlan)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal action_plan: %w", err)
	}

	actionReceiptsJSON, err := json.Marshal(snapshot.ActionReceipts)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal action_receipts: %w", err)
	}

	query := `
		INSERT INTO cycle_snapshots (
			cycle_number, snapshot_timestamp, scoring_params_id,
			initial_vault_value_usd, initial_liquid_usdc, initial_positions,
			target_allocations, action_plan,
			final_vault_value_usd, final_liquid_usdc, final_positions,
			transaction_hashes, action_receipts,
			allocation_efficiency_percent, net_return_usd, total_slippage_usd, total_gas_fee_usd
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING snapshot_id;
	`

	var snapshotID int64
	err = DB.QueryRow(
		query,
		snapshot.CycleNumber, snapshot.Timestamp, snapshot.ScoringParamsID,
		snapshot.InitialVaultValueUSD, snapshot.InitialLiquidUSDC, initialPositionsJSON,
		targetAllocationsJSON, actionPlanJSON,
		snapshot.FinalVaultValueUSD, snapshot.FinalLiquidUSDC, finalPositionsJSON,
		pq.Array(snapshot.TransactionHashes), actionReceiptsJSON,
		snapshot.AllocationEfficiencyPercent, snapshot.NetReturnUSD, snapshot.TotalSlippageUSD, snapshot.TotalGasFeeUSD,
	).Scan(&snapshotID)

	if err != nil {
		return 0, fmt.Errorf("failed to save cycle snapshot: %w", err)
	}

	log.Info().
		Int64("snapshot_id", snapshotID).
		Int("cycle_number", snapshot.CycleNumber).
		Float64("final_vault_value", snapshot.FinalVaultValueUSD).
		Msg("Cycle snapshot saved to database")

	return snapshotID, nil
}

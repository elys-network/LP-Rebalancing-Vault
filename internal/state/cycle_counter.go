/*

This file manages the persistent global cycle counter for the AVM system.
The cycle counter is stored in the database to ensure continuity across restarts.

*/

package state

import (
	"database/sql"
	"fmt"

	"github.com/rs/zerolog/log"
)

// ensureCycleCounterTable creates the cycle_counter table if it doesn't exist
func ensureCycleCounterTable() error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}

	createTableSQL := `
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

	_, err := DB.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create cycle_counter table: %w", err)
	}

	log.Debug().Msg("Ensured cycle_counter table exists")
	return nil
}

// GetCurrentCycleNumber retrieves the current cycle number from the database
func GetCurrentCycleNumber() (int, error) {
	if DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	// Ensure the table exists
	if err := ensureCycleCounterTable(); err != nil {
		return 0, err
	}

	query := `SELECT current_cycle FROM cycle_counter WHERE id = 1;`
	
	var currentCycle int
	row := DB.QueryRow(query)
	err := row.Scan(&currentCycle)

	if err != nil {
		if err == sql.ErrNoRows {
			// This should not happen due to the INSERT in ensureCycleCounterTable
			log.Warn().Msg("No cycle counter row found, initializing to 0")
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get current cycle number: %w", err)
	}

	log.Debug().Int("currentCycle", currentCycle).Msg("Retrieved current cycle number")
	return currentCycle, nil
}

// IncrementCycleNumber increments the cycle counter and returns the new value
func IncrementCycleNumber() (int, error) {
	if DB == nil {
		return 0, fmt.Errorf("database not initialized")
	}

	// Ensure the table exists
	if err := ensureCycleCounterTable(); err != nil {
		return 0, err
	}

	updateQuery := `
		UPDATE cycle_counter 
		SET current_cycle = current_cycle + 1, 
		    updated_at = CURRENT_TIMESTAMP 
		WHERE id = 1 
		RETURNING current_cycle;`

	var newCycle int
	row := DB.QueryRow(updateQuery)
	err := row.Scan(&newCycle)

	if err != nil {
		return 0, fmt.Errorf("failed to increment cycle number: %w", err)
	}

	log.Info().Int("newCycle", newCycle).Msg("Incremented cycle counter")
	return newCycle, nil
}

// ResetCycleNumber resets the cycle counter to a specific value (for testing/maintenance)
func ResetCycleNumber(cycleNumber int) error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}

	// Ensure the table exists
	if err := ensureCycleCounterTable(); err != nil {
		return err
	}

	if cycleNumber < 0 {
		return fmt.Errorf("cycle number cannot be negative: %d", cycleNumber)
	}

	updateQuery := `
		UPDATE cycle_counter 
		SET current_cycle = $1, 
		    updated_at = CURRENT_TIMESTAMP 
		WHERE id = 1;`

	result, err := DB.Exec(updateQuery, cycleNumber)
	if err != nil {
		return fmt.Errorf("failed to reset cycle number to %d: %w", cycleNumber, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no rows updated when resetting cycle number")
	}

	log.Warn().Int("cycleNumber", cycleNumber).Msg("Reset cycle counter")
	return nil
} 
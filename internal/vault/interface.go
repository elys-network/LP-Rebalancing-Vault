package vault

import (
	"github.com/elys-network/avm/internal/types"
)

// VaultManager defines the interface for interacting with the vault system.
// This interface abstracts away the specific implementation details of vault operations,
// allowing for different vault implementations (live, simulation, etc.).
type VaultManager interface {
	// GetLiquidUSDC returns the current amount of liquid USDC available in the vault.
	GetLiquidUSDC() (float64, error)

	// GetPoolPositions returns all current LP positions in pools.
	GetPoolPositions() ([]types.Position, error)

	// GetNonPoolPositions returns all non-pool token positions (e.g., liquid tokens other than USDC).
	GetNonPoolPositions() ([]types.TokenPosition, error)

	// GetTotalVaultValue returns the total USD value of all assets in the vault.
	GetTotalVaultValue() (float64, error)

	// GetTradableDenoms returns all tradable token denoms in the vault.
	// The vault has permission only to trade certain tokens decided by governance.
	GetTradableDenoms() ([]string, error)

	// ExecuteActionPlan executes a list of SubActions and returns transaction details.
	// This is the main method for implementing rebalancing decisions.
	ExecuteActionPlan(subActions []types.SubAction) (*types.TransactionResult, error)

	// Close cleans up any resources used by the vault manager.
	Close() error

	// ensureConnection ensures we have a valid gRPC connection
	ensureConnection() error
}

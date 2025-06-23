# AVM (Autonomous Vault Manager) Package

This package contains the AVM Core logic for the Autonomous Vault Manager system. The AVM uses dependency injection patterns for testability, maintainability, and separation of concerns.

## Architecture

### AVM Struct
The `AVM` struct encapsulates all dependencies and state required for the Autonomous Vault Manager:

```go
type AVM struct {
    // Core dependencies
    logger       zerolog.Logger
    vault        vault.VaultManager
    grpcClient   *grpc.ClientConn
    scoringParams *types.ScoringParameters
    
    // Configuration
    configName    string
    configVersion int
    
    // Runtime state
    cycleCount int
}
```

### Dependency Injection
Dependencies are injected through the `Config` struct during initialization:

```go
type Config struct {
    GRPCClient     *grpc.ClientConn
    VaultManager   vault.VaultManager
    ScoringParams  *types.ScoringParameters
    ConfigName     string
    ConfigVersion  int
}
```

## Key Methods

### `NewAVM(cfg Config) (*AVM, error)`
Creates a new AVM instance with comprehensive validation of all dependencies.

### `RunLoop(ctx context.Context, interval time.Duration)`
Starts the main AVM loop that executes cycles at the specified interval. Supports graceful shutdown through context cancellation.

### `RunCycle(ctx context.Context)`
Executes a complete AVM rebalancing cycle including:
1. Data fetching (pools, tokens)
2. Vault state assessment
3. Pool analysis and scoring
4. Action planning
5. Action execution (withdrawals and deposits)
6. Performance metrics calculation

## Usage Example

```go
// Create AVM configuration
avmConfig := avm.Config{
    GRPCClient:    grpcClient,
    VaultManager:  vaultManager,
    ScoringParams: scoringParams,
    ConfigName:    avm.DEFAULT_SCORING_CONFIG_NAME,
    ConfigVersion: avm.DEFAULT_SCORING_CONFIG_VERSION,
}

// Create AVM instance
avmInstance, err := avm.NewAVM(avmConfig)
if err != nil {
    log.Fatal().Err(err).Msg("Failed to create AVM instance")
}

// Start the main loop
ctx := context.Background()
avmInstance.RunLoop(ctx, 10*time.Minute)
```

## Core Methods

The AVM struct provides the following core functionality:

- **Cycle Management**: `getCycleNumber()`, `getScoringParamsID()`
- **Financial Calculations**: `calculateActualAmountUSD()`, `calculateAllocationEfficiency()`
- **State Management**: `captureVaultState()`, `convertToPositionSnapshots()`
- **Snapshot Handling**: `finalizeFailedSnapshot()`, `saveCycleSnapshot()`
- **Logging**: `logEndOfCycleState()`

## Constants

- `DEFAULT_SCORING_CONFIG_NAME`: Default configuration name for scoring parameters
- `DEFAULT_SCORING_CONFIG_VERSION`: Default version for scoring configuration

These constants are exported for use in other packages that need to reference the default configuration.

## Features

- **Comprehensive Validation**: All dependencies are validated at initialization
- **Graceful Error Handling**: Proper error handling throughout all cycles
- **Structured Logging**: Cycle-specific logging with unique IDs for tracing
- **Performance Tracking**: Detailed metrics for each rebalancing cycle
- **Flexible Configuration**: Easy to configure for different environments 
# LP Yield Optimizer Vault - Developer Notes

This document contains notes, common commands, and architectural insights for developers actively working on the (LP Yield Optimizer Vault). For a user-facing guide, see `README.md`. For a high-level overview, see `ARCHITECTURE.md`.

## Development Environment Setup

1.  **Follow the `README.md`**: Complete all steps in the main README, including cloning, installing dependencies, and setting up the `.env` file.
3.  **Crucial Environment Variables**:
    *   `LOG_LEVEL=debug`: This is your most important debugging tool. It provides verbose output for every step of the AVM cycle.
    *   `AVM_MODE=live`: **The system will not run without this.** This is a safety switch to prevent accidental execution.
    *   `CRYPTOCOMPARE_API`: The system will fail during data fetching if this is not set. A free key is sufficient for development.

## Common Development Commands

```sh
# Run the main AVM service with a full execution loop
go run ./cmd/avm/main.go

# Reset the database to a clean state (drops all tables)
go run ./scripts/reset_db.go

# Build the production binary
go build -o avm-service ./cmd/avm
```

## Codebase Deep Dive & Key Concepts

### The AVM Cycle (`cmd/avm/main.go`)

The entire logic is orchestrated within the `runAVMCycle` function.
1.  `datafetcher.GetPools` & `datafetcher.GetTokens`: Gathers all external data. This is the most network-intensive part.
2.  `vm.Get...`: Queries the blockchain for the vault's current state.
3.  `analyzer.CalculatePoolScores`: The "brain" applies the scoring model.
4.  `analyzer.DetermineTargetAllocations`: The portfolio is constructed here.
5.  `planner.GenerateActionPlan`: The high-level strategy is converted into concrete steps.
6.  `vm.ExecuteActionPlan`: The steps are executed in two phases (withdrawals, then deposits).
7.  `state.SaveCycleSnapshot`: The results are persisted to the database.

### Zero-Tolerance Validation

A core design principle is "fail fast and safe." You will see extensive validation in:
-   **`internal/datafetcher`**: Every piece of data from an external API or on-chain query is scrutinized. If data is missing or invalid (e.g., negative price, `NaN` value), the process for that asset or pool fails immediately. We do not proceed with incomplete data.
-   **`internal/wallet`**: All transaction messages are validated using `ValidateBasic()` before being signed.
-   **`internal/planner`**: All inputs to the planner are validated to prevent planning based on corrupted state.

### Slippage Protection: A Two-Part Mechanism

This is the most critical safety feature. It is **not** just a simple check.
1.  **Simulation (`internal/simulations`)**: The `planner` first calls the simulation functions to get an `Expected...` outcome (e.g., `ExpectedTokenOut`, `ExpectedSharesOut`). This is a prediction based on the current chain state.
2.  **Enforcement (`internal/wallet/transactions.go`)**: The `transactions` builder takes the `Expected...` value and the `SlippageTolerancePct` from the parameters. It calculates the absolute *minimum acceptable outcome* (e.g., `minAmountOut = expectedAmount * (1 - slippageTolerance)`). This minimum value is then embedded directly into the final transaction message sent to the chain. The chain itself will cause the transaction to fail if this minimum is not met.

### The `VaultManager` Interface (`internal/vault/interface.go`)

This abstraction is key for testability. It decouples the core logic (analyzer, planner) from the live implementation. To test the full `runAVMCycle` loop, you can create a `mockVault` that implements this interface and simulates transaction outcomes without needing a live chain or wallet.

## Debugging Guide

1.  **Check the Logs**: The first step is always to set `LOG_LEVEL=debug` in your `.env` file and re-run the cycle. The logs are verbose and component-specific, which helps narrow down where an issue occurred.
2.  **Check the Dashboard**: Open `http://localhost:8080`. The dashboard is the best way to see the *results* of a cycle. Did the vault value drop unexpectedly? Were there any actions in the "Recent Cycles" table?
3.  **Inspect the Database**: If a cycle completed but the results look wrong, inspect the `cycle_snapshots` table directly using `psql`. You can `jsonb_pretty` to view the `action_plan` and `action_receipts` JSON columns to see exactly what the AVM intended to do and what it recorded as the result.
4.  
## TODOs & Future Work (Developer Roadmap)

-   [ ] **Implement Parameter Optimizer**:
    -   Create a new `internal/optimizer` package.
    -   The main function should take recent `CycleSnapshot` data and the current `ScoringParameters`.
    -   It should analyze the performance contribution of different factors (e.g., did pools with high `EdenWeight` underperform?) and suggest small, incremental changes to the parameters.
    -   Integrate this into the main loop in `cmd/avm/main.go` to run every `N` cycles.

-   [ ] **Implement Correlation Analysis**:
    -   In `datafetcher`, add logic to calculate a correlation matrix for the historical returns of the main assets in the pools.
    -   In `analyzer/SelectTopPools.go`, modify `DetermineTargetAllocations` to use this matrix to penalize or cap allocations to highly correlated assets, improving diversification.

-   [ ] **Implement Testing Suite**:
    -   **Unit Tests**: Start with pure functions in `internal/utils`, `internal/analyzer`, and `internal/planner`.
    -   **Integration Tests**: Create a `mockVault` and write a test for the full `runAVMCycle` loop to verify the end-to-end logic without broadcasting transactions.

## Gotchas & Known Issues

-   **API Rate Limiting**: The CryptoCompare API has rate limits. The `FetchHistoricalPriceData` function has basic retry logic, but if you run many cycles in rapid succession during development, you may get temporarily blocked.
-   **Keyring Backend**: The default `test` keyring backend is unencrypted and not suitable for production. A production deployment would require switching to the `os` backend (with a strong password) or integrating with a hardware security module (HSM).
-   **Gas Simulation Failures**: The code currently falls back to a default gas limit if the simulation fails. While this is a safe fallback, frequent simulation failures indicate a problem with the RPC node or the transaction structure and should be investigated.
-   **State Drift on Crash**: If the AVM crashes mid-execution (after withdrawals but before deposits), the vault will be left in a consolidated USDC state. The next cycle will start from this state and should correct it, but this is a known complexity of autonomous systems.
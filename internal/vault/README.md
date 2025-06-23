# internal/vault

## Overview

The `vault` module is responsible for managing the AVM's portfolio state and executing rebalancing plans. It provides a standard interface for interacting with both simulated and live on-chain vaults.

## Key Responsibilities

-   **Define Vault Interface:** Specifies the `VaultManager` interface, which defines the standard set of operations for any vault (e.g., `GetCurrentPositions`, `ExecuteActions`).
-   **Simulated Vault:** Provides a `SimulatedVault` implementation of the `VaultManager` interface. This allows for complete, in-memory simulation of the AVM's strategy without risking real funds.
-   **Live Vault:** Provides a `LiveVault` (or `VaultClient`) implementation of the `VaultManager` interface. This implementation interacts with the `wallet` module to sign and broadcast real transactions.
-   **State Management:** The implementations are responsible for tracking the vault's state, including its LP positions and liquid asset balances.

## Core Components

-   `VaultManager` interface: The contract for all vault implementations.
-   `SimulatedVault` struct: An in-memory vault for testing and simulation.
-   `LiveVault` struct: The implementation for interacting with a real on-chain vault.
-   `ExecuteActions(...)` method: The core method that processes an `ActionPlan` and updates the vault's state (either in-memory or on-chain).

## Notes

-   The interface-based design is critical. It allows `main.go` to easily switch between simulation mode and live trading mode by simply instantiating a different implementation of `VaultManager`.
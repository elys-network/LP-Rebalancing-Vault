# internal/simulations

## Overview

The `simulations` module serves as a read-only client for **simulating** AMM interactions with RPC.
Ideally it should use gRPC, but at the time of writing this there were errors involving decimal modules

## Key Responsibilities

-   **Simulate Swaps:** Provides a function to estimate the outcome of a token swap, including the amount of token out and the expected slippage.
-   **Simulate LP Deposits:** Provides a function to estimate the number of LP shares that will be minted for a given deposit of assets.
-   **Simulate LP Withdrawals:** Provides a function to estimate the underlying assets that will be returned for withdrawing a specific number of LP shares.
-   **RPC Abstraction:** Wraps the complexities of making `abci_query` calls over HTTP POST, including Protobuf marshaling and Base64 decoding.

## Core Components

-   `SimulateSwap(...)`: Estimates the result of a swap.
-   `SimulateJoinPool(...)`: Estimates the result of an LP deposit.
-   `SimulateLeavePool(...)`: Estimates the result of an LP withdrawal.


## Notes

-   **Crucially, this module does NOT sign or broadcast transactions.** Its sole purpose is to provide estimations for the `planner` module to make informed decisions about slippage and expected outcomes.
-   The actual transaction signing and broadcasting is handled by the `internal/wallet` and `internal/vault` (LiveVault) packages.
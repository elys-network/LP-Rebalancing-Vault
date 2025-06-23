# internal/planner

## Overview

The `planner` module acts as the strategic brain of the AVM, translating the high-level portfolio targets from the `analyzer` into a concrete, ordered sequence of executable actions.

## Key Responsibilities

-   **Generate Action Plan:** The primary function is to compare the current vault state (positions, liquid assets) with the target allocations.
-   **Formulate Strategy:** Implements the specific rebalancing strategy. The current strategy is:
    1.  Plan withdrawals from over-allocated pools directly to USDC (single-sided exit).
    2.  Plan single-sided deposits using only USDC into under-allocated pools.
-   **Slippage Management:** Simulates potential actions to estimate slippage and adjusts action sizes to stay within acceptable limits defined in `ScoringParameters`.
-   **Produce Executable Steps:** Outputs an `ActionPlan` struct containing a list of `SubAction`s (e.g., `WITHDRAW_LP`, `DEPOSIT_LP`) in the correct order for execution.

## Notes

-   This module is stateless and should be composed of pure functions. It does not modify the vault state itself; it only produces a *plan* to do so.
-   The logic here is complex, especially the iterative slippage adjustment. Clear comments and logging are essential.
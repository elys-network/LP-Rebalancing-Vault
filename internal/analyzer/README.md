# internal/analyzer

## Overview

The `analyzer` module is the core analytical engine of the AVM, responsible for taking raw pool data and transforming it into quantitative scores and strategic allocation targets.

## Key Responsibilities

-   **Pool Scoring:** Implements the multi-factor scoring algorithm to evaluate the risk/reward profile of each liquidity pool.
-   **Pool Selection:** Selects the top-performing pools based on their calculated scores and strategy parameters.
-   **Allocation Calculation:** Determines the optimal target percentage allocation for each selected pool, respecting portfolio constraints like min/max allocation.
-   **Volatility Calculation:** Provides utilities to calculate historical volatility from price data.

## Core Components

-   `CalculatePoolScore(pool types.Pool, params types.ScoringParameters)`: The main entry point for scoring a single pool.
-   `SelectTopPools(scoredPools []types.PoolScoreResult, params types.ScoringParameters)`: Filters and ranks pools by score.
-   `DetermineTargetAllocations(...)`: Calculates the final portfolio percentage targets.
-   `CalculateVolatility(prices []types.PriceData, ...)`: Calculates annualized volatility from historical prices.


## Notes

-   This module should be composed of **pure functions** where possible. It receives data, performs calculations, and returns results without causing side effects or modifying its inputs directly.
-   It is stateless and does not interact with the database or the blockchain directly.
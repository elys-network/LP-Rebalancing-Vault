# internal/datafetcher

## Overview

The `datafetcher` module is the AVM's interface for fetching outside data for pool scoring, responsible for gathering all necessary on-chain and off-chain data.

## Key Responsibilities

-   **Fetch Pool Data:** Queries the Elys via gRPC to get the current state of all liquidity pools, including reserves, TVL, and on-chain parameters.
-   **Fetch Token Data:** Gathers information about all relevant tokens, including their current prices (from oracle or AMM), precision, and IBC denoms.
-   **Fetch Historical Data:** Connects to external APIs (e.g., CryptoCompare) to retrieve historical price data required for volatility calculations.
-   **Data Aggregation:** Combines data from multiple sources into the clean, unified `types.Pool` and `types.Token` structs used by the rest of the system.

## Core Components

-   `GetPools(grpcClient *grpc.ClientConn)`: Fetches and constructs all `types.Pool` objects.
-   `GetTokens(grpcClient *grpc.ClientConn)`: Fetches and constructs all `types.Token` objects, including their calculated volatility.
-   `FetchCoins30dHourlyPrices(coin string)`: Retrieves historical price data from an external API.


## Notes

-   This module is responsible for handling potential network errors and API inconsistencies gracefully.
-   It contains logic to normalize data from different sources, such as calculating proportional pool weights from raw on-chain reserves and prices.
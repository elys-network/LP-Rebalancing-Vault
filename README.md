# Elys Network: LP Yield Optimizer Vault

An autonomous vault manager designed to strategically manage assets within liquidity pools. The AVM's primary goal is to optimize for risk adjusted returns by automating data collection, analysis, portfolio construction, and transaction execution.

## Core Features

*   **Dynamic Pool Scoring**: Evaluates liquidity pools based on a multiple factor model including weighted APR, impermanent loss risk, volatility, TVL, and trading volume.
*   **Automated Rebalancing**: Generates and executes transaction plans to align the vault's portfolio with target allocations.
*   **Advanced Risk Management**:
    *   Calculates and penalizes for Impermanent Loss (IL) risk and asset volatility.
    *   Leverages on-chain simulations to predict slippage and transaction outcomes *before* execution.
    *   Enforces strict slippage tolerance on all trades.
    *   Applies configurable constraints for minimum/maximum allocation per pool.
*   **Cost-Efficient Execution**: Simulates transactions to estimate the precise gas required, minimizing waste while preventing "out of gas" errors.
*   **Comprehensive Observability**: Features a built-in web dashboard for real-time monitoring of vault performance, recent cycles, and all executed actions.
*   **Persistent State & Analytics**: Records a detailed snapshot of every operational cycle to a PostgreSQL database, enabling historical performance analysis.

## Architecture Overview

The AVM operates in a continuous, cyclical loop. Each cycle consists of distinct phases, managed by specialized components:

1.  **Data Fetching**: Gathers on-chain data (pool states, APRs) and off-chain data (historical prices, volume).
2.  **Analysis & Scoring**: Processes the raw data to score each pool's attractiveness.
3.  **Portfolio Planning**: Selects the top pools and determines the ideal target allocations based on their scores and risk constraints.
4.  **Action Planning**: Creates a concrete, multi-step transaction plan to shift the current portfolio towards the target.
5.  **Execution**: Safely signs and broadcasts the transactions to the Elys Network.
6.  **State Recording**: Saves a comprehensive snapshot of the entire cycle to the database.

For a more detailed breakdown, please see the [ARCHITECTURE.md](ARCHITECTURE.md) file.

## Getting Started

Follow these instructions to get the LP Rebalancing service up and running on your local machine for development and testing.

### Prerequisites

*   [Go](https://golang.org/dl/)
*   [PostgreSQL](https://www.postgresql.org/download/)

### Installation

1.  **Clone the repository:**
    ```sh
    git clone https://github.com/elys-network/lp-rebalancing-vault.git
    cd avm
    ```

2.  **Install Go dependencies:**
    ```sh
    go mod tidy
    ```

### Configuration

The LP Yield Optimizer Vault is configured using environment variables. Create a `.env` file in the root of the project by copying the example file.

```sh
cp .env.example .env
```

Now, edit the `.env` file with your specific configuration:

### Building the LP Yield Optimizer Vault

Compile the main application binary:

```sh
go build -o avm-service ./cmd/avm
```

## Running the AVM

Once built and configured, you can start the service:

```sh
./avm-service
```

The LP Yield Optimizer Vault will start its main operational loop. You can monitor its activity through the console logs and the web dashboard.

## ⚠️ License

Copyright (c) 2025 Elys Network PTE LTD and Elys Network Inc.

This project is licensed under the **Elys Network Business Source License 1.1**.

This is a **source-available** license. You are free to copy, modify, and use this software for **non-production** purposes (e.g., development, testing, research).

**Production use of this software is strictly prohibited** without an explicit "Additional Use Grant" from Elys Network.

On the Change Date, the license will automatically convert to the **GNU General Public License v3.0 (GPL-3.0)**.

For a detailed explanation of your rights and obligations, please see the [**LICENSE.md**](LICENSE.md).
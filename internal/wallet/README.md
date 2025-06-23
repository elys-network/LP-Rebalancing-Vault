# internal/wallet

## Overview

The `wallet` module is the **sole security-critical component** responsible for all on-chain transaction operations. It handles key management, transaction signing, and broadcasting to the network.

## Key Responsibilities

-   **Key Management:** Interacts with the Cosmos SDK `keyring` to securely access the AVM's private key from a specified backend (e.g., `test`, `file`, `os`).
-   **Transaction Building:** Converts abstract `types.SubAction`s into, elys message types that the `vaults` module can execute.
-   **Transaction Signing:** Uses the loaded private key to sign constructed transactions, preparing them for broadcast.
-   **Transaction Broadcasting:** Submits the signed transaction to a Tendermint RPC node and returns the on-chain response (`sdk.TxResponse`).
-   **Slippage Protection:** Implements client-side slippage protection by using simulation results (from `types.SubAction`) to calculate and embed a `MinAmountOut` or `MinSharesOut` into the transaction messages.

## Core Components

-   **`SigningClient`:** A struct that encapsulates the Cosmos SDK `client.Context`, `tx.Factory`, and `keyring`. It manages the account sequence number and is responsible for the low-level signing and broadcasting logic.
-   **`TransactionBuilder`:** A higher-level utility that uses a `SigningClient`. Its primary role is to convert the AVM's strategic `SubAction` plan into a list of `sdk.Msg`s.
-   **`ProcessSubActions(...)`:** The main entry point of the module. It orchestrates the conversion of sub-actions to messages and then signs and broadcasts the resulting transaction.

## Security Notes

-   **This is the only module in the entire AVM system that should ever access or handle private key material.**
-   Configuration for this module, especially `KeyName` and `KeyringDir`, must be managed securely and should not be hardcoded for production environments.
-   Gas and fee settings are configured in the `config` package. These should be monitored to ensure transactions do not fail due to insufficient fees.
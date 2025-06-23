/*
Crypto Compare is used for 30d price data.

This file contains the mapping of coin symbols to their corresponding Crypto Compare ID.
Currently all the coin's symbol are the same as the Crypto Compare ID.

If a coin doesnt have an entry here it will by default use the symbol as the CCID. Because odds are it will work.

But for best practices try to keep this up to date.
It exists JUST IN CASE a coins symbol is different from the Crypto Compare ID.

*/

package config

var (
	CoinToCCId = map[string]string{
		"PAXG":  "PAXG",
		"STARS": "STARS",
		"WBTC":  "WBTC",
		"KAVA":  "KAVA",
		"TIA":   "TIA",
		"STRD":  "STRD",
		"OSMO":  "OSMO",
		"WETH":  "ETH",
		"XION":  "XION",
		"AKT":   "AKT",
		"BLD":   "BLD",
		"NTRN":  "NTRN",
		"OM":    "OM",
		"SAGA":  "SAGA",
		"ATOM":  "ATOM",
		"USDT":  "USDT",
		"SCRT":  "SCRT",
		"ELYS":  "ELYS",
		"USDC":  "USDC",
		"BABY":  "BABY",
		"FET":   "FET",

		"WRAPPED BITCOIN":  "WBTC", // This is for TESTNET compatibility
		"WRAPPED ETHEREUM": "ETH",  // This is for TESTNET compatibility
	}
)

/*

This is a custom type for tokens which contains all the state needed for assisting in scoring pools.

*/

package types

import "time"

type Token struct {
	Symbol        string      `json:"symbol"`         // e.g., "atom"
	Denom         string      `json:"denom"`          // e.g., "uatom"
	IBCDenom      string      `json:"ibc_denom"`      // e.g., "ibc/273...A8"
	Precision     int         `json:"precision"`      // e.g., 1000000 = 1 Token (6 decimal precision)
	PriceUSD      float64     `json:"price_usd"`      // e.g., 1.0
	OracleSourced bool        `json:"oracle_sourced"` // e.g., Meaning if the price in USD is sourced from the Elys oracle
	PriceData     []PriceData `json:"price_data"`     // e.g., historical price data
	Volatility    float64     `json:"volatility"`     // Using the above data, each token gets a volatility score
}

// PriceData holds historical price info
type PriceData struct {
	Timestamp time.Time `json:"timestamp"`
	Price     float64   `json:"price"`
}

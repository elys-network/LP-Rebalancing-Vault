package config

import (
	"github.com/rs/zerolog/log"
)

// Endpoint configuration loaded from environment variables.
// These are populated at startup by the LoadConfig function.
var (
	// NodeRPC is the RPC endpoint for the Elys node.
	NodeRPC string
	// NodeGRPC is the gRPC endpoint for the Elys node.
	NodeGRPC string
	// SupplyAPI is the API endpoint for supply data.
	SupplyAPI string
)

// loadEndpointConfig loads endpoint configuration from environment variables.
// This function is called by LoadConfig() in General.go.
func loadEndpointConfig() error {
	log.Info().Msg("Loading endpoint configuration from environment variables...")

	var err error

	NodeRPC, err = getEnv("NODE_RPC")
	if err != nil {
		return err
	}

	NodeGRPC, err = getEnv("NODE_GRPC")
	if err != nil {
		return err
	}

	SupplyAPI, err = getEnv("SUPPLY_API")
	if err != nil {
		return err
	}

	log.Debug().
		Str("NodeRPC", NodeRPC).
		Str("NodeGRPC", NodeGRPC).
		Str("SupplyAPI", SupplyAPI).
		Msg("Endpoint configuration loaded successfully.")

	return nil
}

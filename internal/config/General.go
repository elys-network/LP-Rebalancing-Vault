package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// AppConfig holds all application configuration loaded from environment variables.
// These are populated at startup by the LoadConfig function.
var (
	// VaultID is the ID of the vault this AVM instance will manage.
	VaultID uint64

	// KeyringBackend is the backend for the keyring (e.g., "os", "file", "test").
	KeyringBackend string
	// KeyringDir is the path to the keyring directory.
	KeyringDir string
	// KeyName is the name of the key within the keyring to use for signing.
	KeyName string

	// ChainID is the chain ID of the target network.
	ChainID string

	// DefaultGasLimit is the fallback gas limit if estimation fails.
	DefaultGasLimit uint64
	// GasAdjustment is the multiplier for simulated gas to ensure sufficient fees.
	GasAdjustment float64
	// GasPriceAmount is the amount of the gas fee denomination per unit of gas.
	GasPriceAmount string
	// GasPriceDenom is the denomination for gas fees.
	GasPriceDenom string
)

// LoadConfig loads configuration from environment variables and sets the global config vars.
// All environment variables are required and must be set.
func LoadConfig() error {
	log.Info().Msg("Loading application configuration from environment variables...")

	var err error

	VaultID, err = getEnvAsUint64("AVM_VAULT_ID")
	if err != nil {
		return err
	}

	KeyringBackend, err = getEnv("KEYRING_BACKEND")
	if err != nil {
		return err
	}

	KeyringDir, err = getEnv("KEYRING_DIR")
	if err != nil {
		return err
	}

	KeyName, err = getEnv("KEYRING_KEY_NAME")
	if err != nil {
		return err
	}

	ChainID, err = getEnv("CHAIN_ID")
	if err != nil {
		return err
	}

	DefaultGasLimit, err = getEnvAsUint64("GAS_DEFAULT_LIMIT")
	if err != nil {
		return err
	}

	GasAdjustment, err = getEnvAsFloat64("GAS_ADJUSTMENT")
	if err != nil {
		return err
	}

	GasPriceAmount, err = getEnv("GAS_PRICE_AMOUNT")
	if err != nil {
		return err
	}

	GasPriceDenom, err = getEnv("GAS_PRICE_DENOM")
	if err != nil {
		return err
	}

	// Load endpoint configuration
	if err := loadEndpointConfig(); err != nil {
		return err
	}

	// Expand the tilde (~) in the keyring directory path to the user's home directory.
	if strings.HasPrefix(KeyringDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		KeyringDir = filepath.Join(home, KeyringDir[2:])
	}

	log.Debug().
		Uint64("VaultID", VaultID).
		Str("ChainID", ChainID).
		Str("KeyName", KeyName).
		Msg("Configuration loaded successfully.")

	return nil
}

// getEnv retrieves a string environment variable. Returns error if not set.
func getEnv(key string) (string, error) {
	if value, exists := os.LookupEnv(key); exists {
		return value, nil
	}
	return "", errors.New("environment variable " + key + " is required but not set")
}

// getEnvAsUint64 retrieves an environment variable as a uint64. Returns error if not set or invalid.
func getEnvAsUint64(key string) (uint64, error) {
	valueStr, err := getEnv(key)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(valueStr, 10, 64)
	if err != nil {
		return 0, errors.New("environment variable " + key + " must be a valid uint64, got: " + valueStr)
	}
	return value, nil
}

// getEnvAsFloat64 retrieves an environment variable as a float64. Returns error if not set or invalid.
func getEnvAsFloat64(key string) (float64, error) {
	valueStr, err := getEnv(key)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, errors.New("environment variable " + key + " must be a valid float64, got: " + valueStr)
	}
	return value, nil
}
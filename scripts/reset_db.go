package main

import (
	"fmt"
	"os"

	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/state"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

func main() {
	// Initialize logger
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logger.Initialize(logLevel)
	log.Info().Msg("Starting database reset script...")

	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("Warning: .env file not found or error loading .env file. Relying on OS environment variables.")
	}

	// Get database configuration from environment variables
	dbHost := os.Getenv("DB_HOST")
	dbPortStr := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USER")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_NAME")
	dbSSLMode := os.Getenv("DB_SSLMODE")

	// Set defaults for missing values
	if dbHost == "" {
		dbHost = "localhost"
	}
	if dbPortStr == "" {
		dbPortStr = "5432"
	}
	if dbUser == "" {
		log.Fatal().Msg("DB_USER environment variable not set.")
	}
	if dbName == "" {
		log.Fatal().Msg("DB_NAME environment variable not set.")
	}
	if dbSSLMode == "" {
		dbSSLMode = "disable"
	}

	// Convert dbPort to integer
	dbPort := 5432
	if dbPortStr != "" {
		fmt.Sscanf(dbPortStr, "%d", &dbPort)
	}

	// Initialize database connection
	dbCfg := state.DBConfig{
		Host:     dbHost,
		Port:     dbPort,
		User:     dbUser,
		Password: dbPassword,
		DBName:   dbName,
		SSLMode:  dbSSLMode,
	}

	log.Info().
		Str("host", dbCfg.Host).
		Int("port", dbCfg.Port).
		Str("user", dbCfg.User).
		Str("dbname", dbCfg.DBName).
		Msg("Connecting to database")

	if err := state.InitDB(dbCfg); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database connection")
	}
	defer state.CloseDB()

	log.Info().Msg("Connected to database. Attempting to drop all tables...")

	// Drop all tables - this is the "reset" part
	dropTablesQuery := `
		DROP TABLE IF EXISTS cycle_snapshots CASCADE;
		DROP TABLE IF EXISTS action_receipts CASCADE;
		DROP TABLE IF EXISTS investment_snapshots CASCADE;
		DROP TABLE IF EXISTS performance_snapshots CASCADE;
		DROP TABLE IF EXISTS scoring_parameters CASCADE;
		DROP TABLE IF EXISTS cycle_counter CASCADE;
	`

	_, err = state.DB.Exec(dropTablesQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to drop tables")
	}
	log.Info().Msg("Successfully dropped all tables")

	// Recreate the schema
	log.Info().Msg("Recreating database schema...")
	if err := state.EnsureSchema(); err != nil {
		log.Fatal().Err(err).Msg("Failed to recreate database schema")
	}
	log.Info().Msg("Database schema successfully recreated")

	log.Info().Msg("Database reset complete!")
}

package main

import (
	"context"
	"crypto/tls"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/elys-network/avm/internal/avm"
	"github.com/elys-network/avm/internal/config"
	datafetcher "github.com/elys-network/avm/internal/datafetcher"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/state"
	"github.com/elys-network/avm/internal/vault"
	"github.com/elys-network/avm/internal/web"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	LOOP_INTERVAL = 10 * time.Minute
)

// main is the entry point for the AVM system.
func main() {
	// --- 1. Initialization Phase ---
	if err := godotenv.Load(); err != nil {
		log.Warn().Msg("Warning: .env file not found. Relying on OS environment variables.")
	}

	// Load configuration from environment variables
	if err := config.LoadConfig(); err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	logger.Initialize(os.Getenv("LOG_LEVEL"))
	log.Info().Msg("AVM Core Logic Starting...")

	// Initialize Database Connection (for ScoringParameters only)
	dbCfg := state.DBConfig{
		Host: os.Getenv("DB_HOST"), Port: mustAtoi(os.Getenv("DB_PORT"), 5432),
		User: os.Getenv("DB_USER"), Password: os.Getenv("DB_PASSWORD"),
		DBName: os.Getenv("DB_NAME"), SSLMode: os.Getenv("DB_SSLMODE"),
	}
	if err := state.InitDB(dbCfg); err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer state.CloseDB()
	if err := state.EnsureSchema(); err != nil {
		log.Fatal().Err(err).Msg("Failed to ensure database schema")
	}

	// Load Scoring Parameters
	scoringParams, err := state.LoadActiveScoringParameters(avm.DEFAULT_SCORING_CONFIG_NAME)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load active scoring parameters, using defaults and saving.")
		defaultParams := config.DefaultScoringParameters
		if _, err := state.SaveScoringParameters(defaultParams, avm.DEFAULT_SCORING_CONFIG_NAME, avm.DEFAULT_SCORING_CONFIG_VERSION, true); err != nil {
			log.Fatal().Err(err).Msg("Failed to save initial default scoring parameters.")
		}
		scoringParams = &defaultParams
	}
	log.Info().Msg("Scoring parameters loaded successfully.")

	// --- Start Web Server ---
	webPort := os.Getenv("WEB_PORT")
	if webPort == "" {
		webPort = "8080"
	}

	webServer := web.NewWebServer(webPort)
	go func() {
		log.Info().Str("port", webPort).Str("url", "http://localhost:"+webPort).Msg("Starting AVM web dashboard")
		if err := webServer.Start(); err != nil {
			log.Error().Err(err).Msg("Web server failed to start")
		}
	}()

	// Initialize gRPC Connection
	grpcEndpoint := config.NodeGRPC
	var creds grpc.DialOption
	if strings.Contains(grpcEndpoint, ":443") {
		creds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{}))
	} else {
		creds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	grpcClient, err := grpc.Dial(grpcEndpoint, creds)
	if err != nil {
		log.Fatal().Err(err).Msg("gRPC connection error")
	}
	defer grpcClient.Close()
	log.Info().Str("endpoint", grpcEndpoint).Msg("gRPC connected")

	// --- 2. Vault Manager Initialization (with Safety Switch) ---
	var vm vault.VaultManager
	avmMode := os.Getenv("AVM_MODE")

	if avmMode == "live" {
		log.Warn().Msg("Initializing AVM in LIVE mode. Real transactions will be broadcast.")
		tokenData, err := datafetcher.GetTokens(grpcClient)
		if err != nil {
			log.Fatal().Err(err).Msg("Cannot start without initial token data")
		}
		liveVault, err := vault.NewVaultClient(config.VaultID, grpcClient, tokenData)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to initialize live vault manager")
		}
		vm = liveVault
	} else {
		log.Fatal().Msg("AVM_MODE is not set to 'live'. Halting to prevent accidental execution. Set AVM_MODE=live to run.")
	}

	// --- 3. Create AVM Instance with Dependency Injection ---
	log.Info().Msg("Creating AVM instance with dependency injection...")
	
	avmConfig := avm.Config{
		GRPCClient:    grpcClient,
		VaultManager:  vm,
		ScoringParams: scoringParams,
		ConfigName:    avm.DEFAULT_SCORING_CONFIG_NAME,
		ConfigVersion: avm.DEFAULT_SCORING_CONFIG_VERSION,
	}

	avmInstance, err := avm.NewAVM(avmConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create AVM instance")
	}

	log.Info().Msg("AVM instance created successfully")

	// --- 4. Start AVM Main Loop ---
	log.Info().Str("interval", LOOP_INTERVAL.String()).Msg("Starting AVM main loop")
	
	// Create context for graceful shutdown
	ctx := context.Background()
	
	// Start the AVM loop (this will run indefinitely)
	avmInstance.RunLoop(ctx, LOOP_INTERVAL)
}

// Helper to convert string to int with a default value
func mustAtoi(s string, defaultValue int) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return defaultValue
	}
	return i
}

package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	elysapp "github.com/elys-network/elys/v6/app"
	"github.com/elys-network/elys/v6/app/params"
	"google.golang.org/grpc"

	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/logger"
	vaulttypes "github.com/elys-network/elys/v6/x/vaults/types"
)

// Error definitions for zero-tolerance error handling
var (
	ErrInvalidConfig           = errors.New("invalid configuration")
	ErrKeyringInit             = errors.New("keyring initialization failed")
	ErrKeyNotFound             = errors.New("signing key not found")
	ErrAddressInvalid          = errors.New("address is invalid")
	ErrRPCConnectionFailed     = errors.New("RPC connection failed")
	ErrGRPCConnectionInvalid   = errors.New("gRPC connection is invalid")
	ErrTxBuildFailed           = errors.New("transaction build failed")
	ErrTxSignFailed            = errors.New("transaction signing failed")
	ErrTxBroadcastFailed       = errors.New("transaction broadcast failed")
	ErrSDKConfigFailed         = errors.New("SDK configuration failed")
	ErrClientContextInvalid    = errors.New("client context is invalid")
	ErrGasSimulationFailed     = errors.New("gas simulation failed")
)

var walletLogger = logger.GetForComponent("wallet_client")

// Thread-safe SDK configuration using sync.Once
var sdkConfigOnce sync.Once
var sdkConfigError error

// SigningClient handles transaction signing and broadcasting with zero-tolerance validation
type SigningClient struct {
	clientCtx    client.Context
	txFactory    tx.Factory
	keyring      keyring.Keyring
	grpcConn     *grpc.ClientConn
	chainID      string
	keyName      string
	fromAddress  sdk.AccAddress
	ownsGRPCConn bool // Track whether we own the connection
}

// NewSigningClient creates a new signing client with comprehensive validation
func NewSigningClient(grpcConn *grpc.ClientConn) (*SigningClient, error) {
	// Validate gRPC connection
	if err := validateGRPCConnection(grpcConn); err != nil {
		return nil, errors.Join(ErrGRPCConnectionInvalid, err)
	}

	// Validate configuration parameters
	if err := validateWalletConfig(); err != nil {
		return nil, errors.Join(ErrInvalidConfig, err)
	}

	// Configure SDK safely
	if err := configureSDK(); err != nil {
		return nil, errors.Join(ErrSDKConfigFailed, err)
	}

	// Initialize keyring with proper validation
	keyring, err := initializeKeyring()
	if err != nil {
		return nil, errors.Join(ErrKeyringInit, err)
	}

	// Get and validate key information
	fromAddress, err := getAndValidateKey(keyring)
	if err != nil {
		return nil, errors.Join(ErrKeyNotFound, err)
	}

	// Create RPC client with validation
	rpcClient, err := createRPCClient()
	if err != nil {
		return nil, errors.Join(ErrRPCConnectionFailed, err)
	}

	// Create encoding config with validation
	encodingConfig, err := createEncodingConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create encoding config: %w", err)
	}

	// Create and validate client context
	clientCtx, err := createClientContext(encodingConfig, keyring, grpcConn, rpcClient, fromAddress)
	if err != nil {
		return nil, errors.Join(ErrClientContextInvalid, err)
	}

	// Create and validate transaction factory
	txFactory, err := createTxFactory(keyring, clientCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction factory: %w", err)
	}

	client := &SigningClient{
		clientCtx:    clientCtx,
		txFactory:    txFactory,
		keyring:      keyring,
		grpcConn:     grpcConn,
		chainID:      config.ChainID,
		keyName:      config.KeyName,
		fromAddress:  fromAddress,
		ownsGRPCConn: false, // We don't own the passed-in connection
	}

	// Final validation of the complete client
	if err := validateSigningClientComplete(client); err != nil {
		return nil, fmt.Errorf("signing client validation failed: %w", err)
	}

	walletLogger.Info().
		Str("address", fromAddress.String()).
		Str("keyName", config.KeyName).
		Str("chainID", config.ChainID).
		Str("rpcEndpoint", config.NodeRPC).
		Msg("Signing client initialized successfully with comprehensive validation")

	return client, nil
}

// validateGRPCConnection validates the gRPC connection
func validateGRPCConnection(grpcConn *grpc.ClientConn) error {
	if grpcConn == nil {
		return errors.New("gRPC connection cannot be nil")
	}
	// Additional gRPC connection state validation could be added here
	return nil
}

// validateWalletConfig validates all wallet configuration parameters
func validateWalletConfig() error {
	if config.ChainID == "" {
		return errors.New("chain ID cannot be empty")
	}
	if config.KeyName == "" {
		return errors.New("key name cannot be empty")
	}
	if config.KeyringDir == "" {
		return errors.New("keyring directory cannot be empty")
	}
	if config.KeyringBackend == "" {
		return errors.New("keyring backend cannot be empty")
	}
	if config.NodeRPC == "" {
		return errors.New("node RPC endpoint cannot be empty")
	}
	if config.DefaultGasLimit == 0 {
		return errors.New("default gas limit cannot be zero")
	}
	if math.IsNaN(config.GasAdjustment) || math.IsInf(config.GasAdjustment, 0) {
		return errors.New("gas adjustment is not finite")
	}
	if config.GasAdjustment <= 0 || config.GasAdjustment > 10 {
		return errors.New("gas adjustment must be between 0 and 10")
	}
	if config.GasPriceAmount == "" {
		return errors.New("gas price amount cannot be empty")
	}
	if config.GasPriceDenom == "" {
		return errors.New("gas price denomination cannot be empty")
	}
	return nil
}

// configureSDK configures the Cosmos SDK safely using sync.Once for thread safety
func configureSDK() error {
	// Configure SDK to use Elys address prefix - only once globally
	sdkConfigOnce.Do(func() {
		sdkConfig := sdk.GetConfig()
		if sdkConfig == nil {
			sdkConfigError = errors.New("failed to get SDK config")
			return
		}

		sdkConfig.SetBech32PrefixForAccount("elys", "elyspub")
		sdkConfig.SetBech32PrefixForValidator("elysvaloper", "elysvaloperpub")
		sdkConfig.SetBech32PrefixForConsensusNode("elysvalcons", "elysvalconspub")
		sdkConfig.Seal()

		walletLogger.Debug().Msg("SDK configuration initialized successfully")
	})
	
	// Return any error that occurred during configuration
	return sdkConfigError
}

// initializeKeyring initializes the keyring with proper validation
func initializeKeyring() (keyring.Keyring, error) {
	// Create keyring directory if it doesn't exist
	if err := os.MkdirAll(config.KeyringDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create keyring directory: %w", err)
	}

	// Create encoding config for keyring
	encodingConfig, err := createEncodingConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create encoding config for keyring: %w", err)
	}

	// Initialize keyring with proper codec
	kr, err := keyring.New(
		"elysd",
		config.KeyringBackend,
		config.KeyringDir,
		os.Stdin,
		encodingConfig.Marshaler,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create keyring: %w", err)
	}

	if kr == nil {
		return nil, errors.New("keyring creation returned nil")
	}

	return kr, nil
}

// getAndValidateKey retrieves and validates the signing key
func getAndValidateKey(kr keyring.Keyring) (sdk.AccAddress, error) {
	// Get key info
	keyInfo, err := kr.Key(config.KeyName)
	if err != nil {
		return nil, fmt.Errorf("key '%s' not found in keyring: %w", config.KeyName, err)
	}

	if keyInfo == nil {
		return nil, fmt.Errorf("key info for '%s' is nil", config.KeyName)
	}

	// Get address from key
	fromAddress, err := keyInfo.GetAddress()
	if err != nil {
		return nil, fmt.Errorf("failed to get address from key: %w", err)
	}

	// Validate address
	if len(fromAddress) == 0 {
		return nil, errors.New("address is empty")
	}

	// Additional address format validation
	if err := sdk.VerifyAddressFormat(fromAddress); err != nil {
		return nil, fmt.Errorf("invalid address format: %w", err)
	}

	return fromAddress, nil
}

// createRPCClient creates and validates RPC client
func createRPCClient() (*rpchttp.HTTP, error) {
	rpcClient, err := rpchttp.New(config.NodeRPC, "/websocket")
	if err != nil {
		return nil, fmt.Errorf("failed to create RPC client: %w", err)
	}

	if rpcClient == nil {
		return nil, errors.New("RPC client creation returned nil")
	}

	return rpcClient, nil
}

// createEncodingConfig creates and validates encoding configuration
func createEncodingConfig() (params.EncodingConfig, error) {
	// Use Elys-specific encoding config
	encodingConfig := elysapp.MakeEncodingConfig()

	// Validate encoding config components
	if encodingConfig.Marshaler == nil {
		return encodingConfig, errors.New("marshaler is nil in encoding config")
	}
	if encodingConfig.InterfaceRegistry == nil {
		return encodingConfig, errors.New("interface registry is nil in encoding config")
	}

	// Register required interfaces
	authtypes.RegisterInterfaces(encodingConfig.InterfaceRegistry)
	codec.RegisterInterfaces(encodingConfig.InterfaceRegistry)

	// Register Elys vault message types - CRITICAL for transaction querying
	vaulttypes.RegisterInterfaces(encodingConfig.InterfaceRegistry)

	return encodingConfig, nil
}

// createClientContext creates and validates client context
func createClientContext(
	encodingConfig params.EncodingConfig,
	kr keyring.Keyring,
	grpcConn *grpc.ClientConn,
	rpcClient *rpchttp.HTTP,
	fromAddress sdk.AccAddress,
) (client.Context, error) {

	// Create tx config
	txConfig := authtx.NewTxConfig(encodingConfig.Marshaler, authtx.DefaultSignModes)
	if txConfig == nil {
		return client.Context{}, errors.New("tx config creation returned nil")
	}

	// Create account retriever
	accountRetriever := authtypes.AccountRetriever{}

	// Create client context
	clientCtx := client.Context{}.
		WithCodec(encodingConfig.Marshaler).
		WithInterfaceRegistry(encodingConfig.InterfaceRegistry).
		WithTxConfig(txConfig).
		WithInput(os.Stdin).
		WithAccountRetriever(accountRetriever).
		WithBroadcastMode(flags.BroadcastSync).
		WithHomeDir(config.KeyringDir).
		WithKeyring(kr).
		WithChainID(config.ChainID).
		WithGRPCClient(grpcConn).
		WithClient(rpcClient).
		WithFromAddress(fromAddress).
		WithFromName(config.KeyName)

	// Validate client context
	if err := validateClientContext(clientCtx); err != nil {
		return client.Context{}, err
	}

	return clientCtx, nil
}

// validateClientContext validates the client context
func validateClientContext(clientCtx client.Context) error {
	if clientCtx.Codec == nil {
		return errors.New("codec is nil in client context")
	}
	if clientCtx.InterfaceRegistry == nil {
		return errors.New("interface registry is nil in client context")
	}
	if clientCtx.TxConfig == nil {
		return errors.New("tx config is nil in client context")
	}
	if clientCtx.Keyring == nil {
		return errors.New("keyring is nil in client context")
	}
	if clientCtx.ChainID == "" {
		return errors.New("chain ID is empty in client context")
	}
	if len(clientCtx.FromAddress) == 0 {
		return errors.New("from address is empty in client context")
	}
	if clientCtx.FromName == "" {
		return errors.New("from name is empty in client context")
	}
	return nil
}

// createTxFactory creates and validates transaction factory
func createTxFactory(kr keyring.Keyring, clientCtx client.Context) (tx.Factory, error) {
	// Create transaction factory with comprehensive validation
	txFactory := tx.Factory{}.
		WithChainID(config.ChainID).
		WithKeybase(kr).
		WithGas(200000).        // Default gas limit
		WithGasAdjustment(1.5). // Gas adjustment factor
		WithSignMode(signing.SignMode_SIGN_MODE_DIRECT).
		WithAccountRetriever(clientCtx.AccountRetriever).
		WithTxConfig(clientCtx.TxConfig)

	// Validate transaction factory
	if err := validateTxFactory(txFactory); err != nil {
		return tx.Factory{}, err
	}

	return txFactory, nil
}

// validateTxFactory validates the transaction factory
func validateTxFactory(txFactory tx.Factory) error {
	if txFactory.ChainID() == "" {
		return errors.New("chain ID is empty in tx factory")
	}
	if txFactory.Keybase() == nil {
		return errors.New("keybase is nil in tx factory")
	}
	if txFactory.Gas() == 0 {
		return errors.New("gas is zero in tx factory")
	}
	if txFactory.GasAdjustment() <= 0 {
		return errors.New("gas adjustment must be positive in tx factory")
	}
	if txFactory.AccountRetriever() == nil {
		return errors.New("account retriever is nil in tx factory")
	}
	return nil
}

// validateSigningClientComplete performs final validation of the complete signing client
func validateSigningClientComplete(client *SigningClient) error {
	if client == nil {
		return errors.New("signing client is nil")
	}
	if client.chainID == "" {
		return errors.New("chain ID is empty")
	}
	if client.keyName == "" {
		return errors.New("key name is empty")
	}
	if len(client.fromAddress) == 0 {
		return errors.New("from address is empty")
	}
	if client.keyring == nil {
		return errors.New("keyring is nil")
	}
	if client.grpcConn == nil {
		return errors.New("gRPC connection is nil")
	}
	if err := validateClientContext(client.clientCtx); err != nil {
		return fmt.Errorf("client context validation failed: %w", err)
	}
	if err := validateTxFactory(client.txFactory); err != nil {
		return fmt.Errorf("tx factory validation failed: %w", err)
	}
	return nil
}

// SignAndBroadcastTx signs and broadcasts a transaction with comprehensive validation
func (s *SigningClient) SignAndBroadcastTx(ctx context.Context, msgs ...sdk.Msg) (*sdk.TxResponse, error) {
	walletLogger.Info().
		Int("messageCount", len(msgs)).
		Msg("SignAndBroadcastTx: Starting transaction signing and broadcasting")

	// Validate inputs
	if ctx == nil {
		walletLogger.Error().Msg("SignAndBroadcastTx: Context cannot be nil")
		return nil, errors.New("context cannot be nil")
	}
	if len(msgs) == 0 {
		walletLogger.Error().Msg("SignAndBroadcastTx: Messages cannot be empty")
		return nil, errors.New("messages cannot be empty")
	}

	walletLogger.Info().Msg("SignAndBroadcastTx: Input validation passed")

	// Validate each message
	walletLogger.Info().Msg("SignAndBroadcastTx: Validating messages...")
	for i, msg := range msgs {
		if msg == nil {
			walletLogger.Error().Int("messageIndex", i).Msg("SignAndBroadcastTx: Message is nil")
			return nil, fmt.Errorf("message %d is nil", i)
		}

		// Validate the message if it has ValidateBasic
		if validator, ok := msg.(interface{ ValidateBasic() error }); ok {
			if err := validator.ValidateBasic(); err != nil {
				walletLogger.Error().Err(err).Int("messageIndex", i).Msg("SignAndBroadcastTx: Message validation failed")
				return nil, fmt.Errorf("message %d validation failed: %w", i, err)
			}
		}
	}

	walletLogger.Info().Msg("SignAndBroadcastTx: Message validation passed")

	// Get account info with proper error handling
	walletLogger.Info().Msg("SignAndBroadcastTx: Retrieving account info...")
	account, err := s.clientCtx.AccountRetriever.GetAccount(s.clientCtx, s.fromAddress)
	if err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Failed to get account info")
		return nil, errors.Join(ErrAccountRetrievalFailed, fmt.Errorf("failed to get account info: %w", err))
	}

	if account == nil {
		walletLogger.Error().Msg("SignAndBroadcastTx: Account is nil")
		return nil, errors.Join(ErrAccountRetrievalFailed, errors.New("account is nil"))
	}

	walletLogger.Info().
		Uint64("accountNumber", account.GetAccountNumber()).
		Uint64("sequence", account.GetSequence()).
		Msg("SignAndBroadcastTx: Account info retrieved successfully")

	// Validate gas configuration before using
	walletLogger.Info().Msg("SignAndBroadcastTx: Validating gas configuration...")
	if err := validateGasConfiguration(); err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Gas configuration validation failed")
		return nil, errors.Join(ErrInvalidConfig, err)
	}

	walletLogger.Info().Msg("SignAndBroadcastTx: Gas configuration validated")

	// Update factory with current account info and validated gas configuration
	walletLogger.Info().Msg("SignAndBroadcastTx: Updating transaction factory...")
	
	// Calculate gas using simulation instead of hardcoded values
	estimatedGas, err := s.CalculateGas(ctx, msgs...)
	if err != nil {
		walletLogger.Warn().Err(err).Msg("SignAndBroadcastTx: Gas estimation failed, using default gas limit")
		// Fall back to default gas if simulation fails
		estimatedGas = config.DefaultGasLimit
	}
	
	s.txFactory = s.txFactory.
		WithAccountNumber(account.GetAccountNumber()).
		WithSequence(account.GetSequence()).
		WithGas(estimatedGas).
		WithGasAdjustment(config.GasAdjustment).
		WithGasPrices(config.GasPriceAmount + config.GasPriceDenom)

	walletLogger.Info().
		Uint64("estimatedGas", estimatedGas).
		Float64("gasAdjustment", config.GasAdjustment).
		Str("gasPrice", config.GasPriceAmount+config.GasPriceDenom).
		Uint64("accountNumber", account.GetAccountNumber()).
		Uint64("sequence", account.GetSequence()).
		Msg("SignAndBroadcastTx: Using simulated gas configuration for transaction")

	// Build unsigned transaction with validation
	walletLogger.Info().Msg("SignAndBroadcastTx: Building unsigned transaction...")
	txBuilder, err := s.txFactory.BuildUnsignedTx(msgs...)
	if err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Failed to build unsigned transaction")
		return nil, errors.Join(ErrTxBuildFailed, fmt.Errorf("failed to build unsigned tx: %w", err))
	}

	if txBuilder == nil {
		walletLogger.Error().Msg("SignAndBroadcastTx: Transaction builder is nil")
		return nil, errors.Join(ErrTxBuildFailed, errors.New("tx builder is nil"))
	}

	walletLogger.Info().Msg("SignAndBroadcastTx: Unsigned transaction built successfully")

	// Sign transaction with validation
	walletLogger.Info().Msg("SignAndBroadcastTx: Signing transaction...")
	err = tx.Sign(ctx, s.txFactory, s.clientCtx.GetFromName(), txBuilder, true)
	if err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Failed to sign transaction")
		return nil, errors.Join(ErrTxSignFailed, fmt.Errorf("failed to sign transaction: %w", err))
	}

	walletLogger.Info().Msg("SignAndBroadcastTx: Transaction signed successfully")

	// Encode transaction with validation
	walletLogger.Info().Msg("SignAndBroadcastTx: Encoding transaction...")
	txBytes, err := s.clientCtx.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Failed to encode transaction")
		return nil, errors.Join(ErrTxBuildFailed, fmt.Errorf("failed to encode transaction: %w", err))
	}

	if len(txBytes) == 0 {
		walletLogger.Error().Msg("SignAndBroadcastTx: Encoded transaction is empty")
		return nil, errors.Join(ErrTxBuildFailed, errors.New("encoded transaction is empty"))
	}

	walletLogger.Info().
		Int("txBytesLength", len(txBytes)).
		Msg("SignAndBroadcastTx: Transaction encoded successfully")

	// Broadcast transaction with validation
	walletLogger.Info().Msg("SignAndBroadcastTx: Broadcasting transaction...")
	res, err := s.clientCtx.BroadcastTx(txBytes)
	if err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Failed to broadcast transaction")
		return nil, errors.Join(ErrTxBroadcastFailed, fmt.Errorf("failed to broadcast transaction: %w", err))
	}

	walletLogger.Info().
		Str("txHash", res.TxHash).
		Msg("SignAndBroadcastTx: Transaction broadcasted successfully")

	// Validate transaction response
	if err := validateTransactionResponse(res); err != nil {
		walletLogger.Error().Err(err).Msg("SignAndBroadcastTx: Transaction response validation failed")
		return nil, errors.Join(ErrTxBroadcastFailed, err)
	}

	walletLogger.Info().
		Str("txHash", res.TxHash).
		Uint32("code", res.Code).
		Str("rawLog", res.RawLog).
		Int("messageCount", len(msgs)).
		Msg("SignAndBroadcastTx: Transaction broadcasted successfully")

	return res, nil
}

// validateGasConfiguration validates gas-related configuration
func validateGasConfiguration() error {
	if config.DefaultGasLimit == 0 {
		return errors.New("default gas limit cannot be zero")
	}
	if math.IsNaN(config.GasAdjustment) || math.IsInf(config.GasAdjustment, 0) {
		return errors.New("gas adjustment is not finite")
	}
	if config.GasAdjustment <= 0 {
		return errors.New("gas adjustment must be positive")
	}
	if config.GasPriceAmount == "" {
		return errors.New("gas price amount cannot be empty")
	}
	if config.GasPriceDenom == "" {
		return errors.New("gas price denomination cannot be empty")
	}
	return nil
}

// validateTransactionResponse validates the transaction response
func validateTransactionResponse(res *sdk.TxResponse) error {
	if res == nil {
		return errors.New("transaction response is nil")
	}
	if res.TxHash == "" {
		return errors.New("transaction hash is empty")
	}
	// Note: We don't check res.Code here as it might be non-zero for failed transactions
	// The caller should check the code if they need to determine success/failure
	return nil
}

// GetAddress returns the signing address with validation
func (s *SigningClient) GetAddress() sdk.AccAddress {
	return s.fromAddress
}

// GetAddressString returns the signing address as string with validation
func (s *SigningClient) GetAddressString() string {
	if len(s.fromAddress) == 0 {
		walletLogger.Warn().Msg("From address is empty")
		return ""
	}
	return s.fromAddress.String()
}

// Close closes the gRPC connection safely
func (s *SigningClient) Close() error {
	if s.ownsGRPCConn && s.grpcConn != nil {
		err := s.grpcConn.Close()
		if err != nil {
			walletLogger.Error().Err(err).Msg("Failed to close gRPC connection")
			return fmt.Errorf("failed to close gRPC connection: %w", err)
		}
		walletLogger.Debug().Msg("gRPC connection closed successfully")
	}
	return nil
}

// QueryTxByHash queries a transaction by its hash to get complete execution details
func (s *SigningClient) QueryTxByHash(ctx context.Context, txHash string) (*sdk.TxResponse, error) {
	if txHash == "" {
		return nil, errors.New("transaction hash cannot be empty")
	}

	// Validate client context
	if err := validateClientContext(s.clientCtx); err != nil {
		return nil, fmt.Errorf("client context validation failed: %w", err)
	}

	// Query the transaction by hash
	txResponse, err := authtx.QueryTx(s.clientCtx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to query transaction %s: %w", txHash, err)
	}

	if txResponse == nil {
		return nil, fmt.Errorf("transaction %s not found", txHash)
	}

	walletLogger.Debug().
		Str("txHash", txHash).
		Int64("gasUsed", txResponse.GasUsed).
		Int64("gasWanted", txResponse.GasWanted).
		Int("eventCount", len(txResponse.Events)).
		Msg("Successfully queried transaction by hash")

	return txResponse, nil
}

// CalculateGas simulates a transaction to estimate gas usage with comprehensive validation
func (s *SigningClient) CalculateGas(ctx context.Context, msgs ...sdk.Msg) (uint64, error) {
	walletLogger.Info().
		Int("messageCount", len(msgs)).
		Msg("CalculateGas: Starting gas estimation simulation")

	// Validate inputs
	if ctx == nil {
		walletLogger.Error().Msg("CalculateGas: Context cannot be nil")
		return 0, errors.New("context cannot be nil")
	}
	if len(msgs) == 0 {
		walletLogger.Error().Msg("CalculateGas: Messages cannot be empty")
		return 0, errors.New("messages cannot be empty")
	}

	// Validate each message
	for i, msg := range msgs {
		if msg == nil {
			walletLogger.Error().Int("messageIndex", i).Msg("CalculateGas: Message is nil")
			return 0, fmt.Errorf("message %d is nil", i)
		}

		// Validate the message if it has ValidateBasic
		if validator, ok := msg.(interface{ ValidateBasic() error }); ok {
			if err := validator.ValidateBasic(); err != nil {
				walletLogger.Error().Err(err).Int("messageIndex", i).Msg("CalculateGas: Message validation failed")
				return 0, fmt.Errorf("message %d validation failed: %w", i, err)
			}
		}
	}

	walletLogger.Info().Msg("CalculateGas: Message validation passed")

	// Get account info for simulation
	walletLogger.Info().Msg("CalculateGas: Retrieving account info for simulation...")
	account, err := s.clientCtx.AccountRetriever.GetAccount(s.clientCtx, s.fromAddress)
	if err != nil {
		walletLogger.Error().Err(err).Msg("CalculateGas: Failed to get account info")
		return 0, fmt.Errorf("failed to get account info for simulation: %w", err)
	}

	if account == nil {
		walletLogger.Error().Msg("CalculateGas: Account is nil")
		return 0, errors.New("account is nil")
	}

	// Update factory with current account info for simulation
	simulationFactory := s.txFactory.
		WithAccountNumber(account.GetAccountNumber()).
		WithSequence(account.GetSequence()).
		WithGas(0). // Set gas to 0 for simulation
		WithGasAdjustment(config.GasAdjustment).
		WithGasPrices(config.GasPriceAmount + config.GasPriceDenom)

	walletLogger.Info().
		Uint64("accountNumber", account.GetAccountNumber()).
		Uint64("sequence", account.GetSequence()).
		Msg("CalculateGas: Account info retrieved for simulation")

	// Build simulation transaction
	walletLogger.Info().Msg("CalculateGas: Building simulation transaction...")
	txBytes, err := simulationFactory.BuildSimTx(msgs...)
	if err != nil {
		walletLogger.Error().Err(err).Msg("CalculateGas: Failed to build simulation transaction")
		return 0, fmt.Errorf("failed to build simulation transaction: %w", err)
	}

	if len(txBytes) == 0 {
		walletLogger.Error().Msg("CalculateGas: Simulation transaction bytes are empty")
		return 0, errors.New("simulation transaction bytes are empty")
	}

	walletLogger.Info().
		Int("txBytesLength", len(txBytes)).
		Msg("CalculateGas: Simulation transaction built successfully")

	// Import the tx service client
	txSvcClient := txtypes.NewServiceClient(s.grpcConn)
	if txSvcClient == nil {
		walletLogger.Error().Msg("CalculateGas: Failed to create tx service client")
		return 0, errors.New("failed to create tx service client")
	}

	// Create simulation request
	simRequest := &txtypes.SimulateRequest{
		TxBytes: txBytes,
	}

	// Execute simulation
	walletLogger.Info().Msg("CalculateGas: Executing gas simulation...")
	simRes, err := txSvcClient.Simulate(ctx, simRequest)
	if err != nil {
		walletLogger.Error().Err(err).Msg("CalculateGas: Gas simulation failed")
		return 0, fmt.Errorf("gas simulation failed: %w", err)
	}

	if simRes == nil {
		walletLogger.Error().Msg("CalculateGas: Simulation response is nil")
		return 0, errors.New("simulation response is nil")
	}

	if simRes.GasInfo == nil {
		walletLogger.Error().Msg("CalculateGas: Gas info is nil in simulation response")
		return 0, errors.New("gas info is nil in simulation response")
	}

	// Validate simulated gas usage
	simulatedGas := simRes.GasInfo.GasUsed
	if simulatedGas == 0 {
		walletLogger.Error().Msg("CalculateGas: Simulated gas usage is zero")
		return 0, errors.New("simulated gas usage is zero")
	}

	// Calculate adjusted gas amount with validation
	gasAdjustment := simulationFactory.GasAdjustment()
	if gasAdjustment <= 0 {
		walletLogger.Error().Float64("gasAdjustment", gasAdjustment).Msg("CalculateGas: Invalid gas adjustment")
		return 0, fmt.Errorf("invalid gas adjustment: %f", gasAdjustment)
	}

	adjustedGas := uint64(gasAdjustment * float64(simulatedGas))
	if adjustedGas == 0 {
		walletLogger.Error().
			Uint64("simulatedGas", simulatedGas).
			Float64("gasAdjustment", gasAdjustment).
			Msg("CalculateGas: Adjusted gas is zero")
		return 0, errors.New("adjusted gas calculation resulted in zero")
	}

	// Add safety buffer to prevent out-of-gas errors
	finalGas := adjustedGas + 10000 // Add 10k gas buffer

	walletLogger.Info().
		Uint64("simulatedGas", simulatedGas).
		Float64("gasAdjustment", gasAdjustment).
		Uint64("adjustedGas", adjustedGas).
		Uint64("finalGas", finalGas).
		Int("messageCount", len(msgs)).
		Msg("CalculateGas: Gas estimation completed successfully")

	return finalGas, nil
}


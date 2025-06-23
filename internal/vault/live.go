package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc"

	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types"
	"github.com/elys-network/avm/internal/wallet"
	vaulttypes "github.com/elys-network/elys/v6/x/vaults/types"
	"github.com/gogo/protobuf/proto"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Error definitions for zero-tolerance error handling
var (
	ErrInvalidVaultID    = errors.New("vault ID is invalid")
	ErrInvalidConnection = errors.New("connection is invalid")
	ErrInvalidTokenData  = errors.New("token data is invalid")
	ErrInvalidPosition   = errors.New("position data is invalid")
	ErrMathematicalError = errors.New("mathematical calculation error")
	ErrRPCRequestFailed  = errors.New("RPC request failed")
	ErrInvalidResponse   = errors.New("response data is invalid")
	ErrUSDCNotFound      = errors.New("USDC token not found")
	ErrPrecisionError    = errors.New("precision calculation error")
	ErrConnectionFailed  = errors.New("connection establishment failed")
	ErrActionPlanInvalid = errors.New("action plan is invalid")
	ErrTransactionFailed = errors.New("transaction execution failed")
)

var vaultLogger = logger.GetForComponent("vault_client")

// JSON-RPC Structures for RPC calls with validation

// JSONRPCRequest defines the structure of a JSON-RPC request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  ABCIQueryParams `json:"params"`
}

// ABCIQueryParams defines the parameters for the "abci_query" method.
type ABCIQueryParams struct {
	Path   string `json:"path"`
	Data   string `json:"data"` // Hex-encoded string
	Height string `json:"height,omitempty"`
	Prove  bool   `json:"prove,omitempty"`
}

// JSONRPCResponse defines the structure of a JSON-RPC response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  ABCIQueryResult `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// ABCIQueryResult defines the structure of the "result" field for "abci_query".
type ABCIQueryResult struct {
	Response struct {
		Log    string `json:"log"`
		Key    string `json:"key"`   // Base64 encoded
		Value  string `json:"value"` // Base64 encoded
		Height string `json:"height"`
		Code   uint32 `json:"code"` // Add code to check for app-level errors
	} `json:"response"`
}

// JSONRPCError defines the structure of a JSON-RPC error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// VaultClient implements a persistent gRPC client for vault operations with zero-tolerance validation
type VaultClient struct {
	vaultId uint64

	// Persistent gRPC connection
	grpcConn *grpc.ClientConn

	// Vault query client
	queryClient vaulttypes.QueryClient

	// Connection management
	ctx    context.Context
	cancel context.CancelFunc

	Tokens map[string]types.Token
}

// NewVaultClient creates a new vault client with comprehensive validation
func NewVaultClient(vaultId uint64, grpcClient *grpc.ClientConn, tokens map[string]types.Token) (*VaultClient, error) {
	// Validate inputs with zero tolerance
	if err := validateVaultClientInputs(vaultId, grpcClient, tokens); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Validate gRPC connection state
	if err := validateGRPCConnection(grpcClient); err != nil {
		cancel()
		return nil, errors.Join(ErrInvalidConnection, err)
	}

	// Create query client with validation
	queryClient := vaulttypes.NewQueryClient(grpcClient)
	if queryClient == nil {
		cancel()
		return nil, errors.New("failed to create query client")
	}

	client := &VaultClient{
		vaultId:     vaultId,
		grpcConn:    grpcClient,
		queryClient: queryClient,
		ctx:         ctx,
		cancel:      cancel,
		Tokens:      tokens,
	}

	// Final validation of the complete client
	if err := validateVaultClient(client); err != nil {
		cancel()
		return nil, fmt.Errorf("vault client validation failed: %w", err)
	}

	vaultLogger.Info().
		Uint64("vaultId", vaultId).
		Int("tokenCount", len(tokens)).
		Msg("VaultClient initialized successfully with comprehensive validation")

	return client, nil
}

// validateVaultClientInputs performs comprehensive input validation
func validateVaultClientInputs(vaultId uint64, grpcClient *grpc.ClientConn, tokens map[string]types.Token) error {
	if vaultId == 0 {
		return errors.Join(ErrInvalidVaultID, errors.New("vault ID cannot be zero"))
	}
	if grpcClient == nil {
		return errors.Join(ErrInvalidConnection, errors.New("gRPC client cannot be nil"))
	}
	if tokens == nil {
		return errors.Join(ErrInvalidTokenData, errors.New("tokens map cannot be nil"))
	}
	if len(tokens) == 0 {
		return errors.Join(ErrInvalidTokenData, errors.New("tokens map cannot be empty"))
	}

	// Validate token data integrity
	for symbol, token := range tokens {
		if err := validateTokenData(symbol, token); err != nil {
			return errors.Join(ErrInvalidTokenData, err)
		}
	}

	return nil
}

// validateTokenData validates individual token configuration
func validateTokenData(symbol string, token types.Token) error {
	if symbol == "" {
		return errors.New("token symbol cannot be empty")
	}
	if token.Symbol == "" {
		return fmt.Errorf("token %s has empty symbol", symbol)
	}
	if token.Denom == "" {
		return fmt.Errorf("token %s has empty denom", symbol)
	}
	if token.IBCDenom == "" {
		return fmt.Errorf("token %s has empty IBC denom", symbol)
	}
	if token.Precision < 0 || token.Precision > 18 {
		return fmt.Errorf("token %s has invalid precision: %d", symbol, token.Precision)
	}
	if math.IsNaN(token.PriceUSD) || math.IsInf(token.PriceUSD, 0) {
		return fmt.Errorf("token %s has invalid price: %f", symbol, token.PriceUSD)
	}
	if token.PriceUSD < 0 {
		return fmt.Errorf("token %s has negative price: %f", symbol, token.PriceUSD)
	}
	return nil
}

// validateGRPCConnection validates the gRPC connection
func validateGRPCConnection(grpcClient *grpc.ClientConn) error {
	if grpcClient == nil {
		return errors.New("gRPC connection is nil")
	}

	state := grpcClient.GetState()
	if state == connectivity.Shutdown {
		return errors.New("gRPC connection is shutdown")
	}
	if state == connectivity.TransientFailure {
		return errors.New("gRPC connection is in transient failure state")
	}

	return nil
}

// validateVaultClient performs final validation of the vault client
func validateVaultClient(client *VaultClient) error {
	if client == nil {
		return errors.New("vault client is nil")
	}
	if client.vaultId == 0 {
		return errors.New("vault ID is zero")
	}
	if client.grpcConn == nil {
		return errors.New("gRPC connection is nil")
	}
	if client.queryClient == nil {
		return errors.New("query client is nil")
	}
	if client.ctx == nil {
		return errors.New("context is nil")
	}
	if client.cancel == nil {
		return errors.New("cancel function is nil")
	}
	if client.Tokens == nil {
		return errors.New("tokens map is nil")
	}
	return nil
}

// GetLiquidUSDC fetches USDC with comprehensive validation and mathematical safety
func (v *VaultClient) GetLiquidUSDC() (float64, error) {
	// Validate client state
	if err := v.validateClientState(); err != nil {
		return 0, err
	}

	// Ensure connection with timeout
	if err := v.ensureConnection(); err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to ensure gRPC connection for GetLiquidUSDC")
		return 0, errors.Join(ErrConnectionFailed, fmt.Errorf("failed to ensure gRPC connection: %w", err))
	}

	// Create timeout context with validation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ctx == nil {
		return 0, errors.New("failed to create timeout context")
	}

	// Query vault positions with validation
	positions, err := v.queryClient.VaultPositions(ctx, &vaulttypes.QueryVaultPositionsRequest{
		VaultId: v.vaultId,
	})
	if err != nil {
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to query vault positions: %w", err))
	}

	// Validate response
	if err := validatePositionsResponse(positions); err != nil {
		return 0, errors.Join(ErrInvalidResponse, err)
	}

	// Find and validate USDC token
	usdcToken, err := v.findAndValidateUSDCToken()
	if err != nil {
		return 0, err
	}

	// Find USDC position with mathematical safety
	for _, position := range positions.Positions {
		if position.TokenDenom == usdcToken.IBCDenom {
			return v.calculateUSDCAmount(position, usdcToken)
		}
	}

	// Return 0 if USDC position not found (valid case)
	vaultLogger.Debug().Uint64("vaultId", v.vaultId).Msg("No USDC position found in vault")
	return 0, nil
}

// validateClientState validates the client's internal state
func (v *VaultClient) validateClientState() error {
	if v == nil {
		return errors.New("vault client is nil")
	}
	if v.vaultId == 0 {
		return errors.Join(ErrInvalidVaultID, errors.New("vault ID is zero"))
	}
	if v.queryClient == nil {
		return errors.New("query client is nil")
	}
	if v.Tokens == nil {
		return errors.Join(ErrInvalidTokenData, errors.New("tokens map is nil"))
	}

	return nil
}

// validatePositionsResponse validates the positions query response
func validatePositionsResponse(positions *vaulttypes.QueryVaultPositionsResponse) error {
	if positions == nil {
		return errors.New("positions response is nil")
	}
	if positions.Positions == nil {
		return errors.New("positions array is nil")
	}

	// Validate each position
	for i, position := range positions.Positions {
		if err := validatePosition(position, i); err != nil {
			return err
		}
	}

	return nil
}

// validatePosition validates individual position data
func validatePosition(position vaulttypes.PositionToken, index int) error {
	if position.TokenDenom == "" {
		return fmt.Errorf("position %d has empty token denom", index)
	}
	if position.TokenAmount.IsNil() {
		return fmt.Errorf("position %d has nil token amount", index)
	}
	if position.TokenAmount.IsNegative() {
		return fmt.Errorf("position %d has negative token amount", index)
	}
	if position.TokenUsdValue.IsNil() {
		return fmt.Errorf("position %d has nil USD value", index)
	}
	if position.TokenUsdValue.IsNegative() {
		return fmt.Errorf("position %d has negative USD value", index)
	}

	// Validate USD value is finite
	usdValue := position.TokenUsdValue.MustFloat64()
	if math.IsNaN(usdValue) || math.IsInf(usdValue, 0) {
		return fmt.Errorf("position %d has invalid USD value: %f", index, usdValue)
	}

	return nil
}

// findAndValidateUSDCToken finds and validates USDC token configuration
func (v *VaultClient) findAndValidateUSDCToken() (*types.Token, error) {
	var usdcToken *types.Token
	for _, token := range v.Tokens {
		if token.Denom == "uusdc" || token.Symbol == "USDC" {
			tokenCopy := token
			usdcToken = &tokenCopy
			break
		}
	}

	if usdcToken == nil {
		return nil, errors.Join(ErrUSDCNotFound, errors.New("USDC token not found in token configuration"))
	}

	// Validate USDC token data
	if err := validateUSDCToken(*usdcToken); err != nil {
		return nil, errors.Join(ErrUSDCNotFound, err)
	}

	return usdcToken, nil
}

// validateUSDCToken validates USDC-specific token requirements
func validateUSDCToken(token types.Token) error {
	if token.IBCDenom == "" {
		return errors.New("USDC token has empty IBC denom")
	}
	if token.Precision < 0 || token.Precision > 18 {
		return fmt.Errorf("USDC token has invalid precision: %d", token.Precision)
	}
	if math.IsNaN(token.PriceUSD) || math.IsInf(token.PriceUSD, 0) {
		return errors.New("USDC token has invalid price")
	}
	// USDC should be approximately $1
	if math.Abs(token.PriceUSD-1.0) > 0.1 {
		return fmt.Errorf("USDC price is too far from $1: %f", token.PriceUSD)
	}
	return nil
}

// calculateUSDCAmount calculates USDC amount with mathematical safety
func (v *VaultClient) calculateUSDCAmount(position vaulttypes.PositionToken, usdcToken *types.Token) (float64, error) {
	// Parse amount with validation
	rawAmountStr := position.TokenAmount.String()
	if rawAmountStr == "" {
		return 0, errors.Join(ErrMathematicalError, errors.New("token amount string is empty"))
	}

	rawAmount, err := strconv.ParseFloat(rawAmountStr, 64)
	if err != nil {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("failed to parse USDC amount: %w", err))
	}

	// Validate parsed amount
	if math.IsNaN(rawAmount) || math.IsInf(rawAmount, 0) {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("parsed amount is not finite: %f", rawAmount))
	}
	if rawAmount < 0 {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("raw amount is negative: %f", rawAmount))
	}

	// Calculate precision factor with validation
	if usdcToken.Precision < 0 || usdcToken.Precision > 18 {
		return 0, errors.Join(ErrPrecisionError, fmt.Errorf("invalid precision: %d", usdcToken.Precision))
	}

	precisionFactor := math.Pow10(usdcToken.Precision)
	if math.IsNaN(precisionFactor) || math.IsInf(precisionFactor, 0) || precisionFactor <= 0 {
		return 0, errors.Join(ErrPrecisionError, fmt.Errorf("invalid precision factor: %f", precisionFactor))
	}

	// Calculate actual amount with mathematical safety
	actualAmount := rawAmount / precisionFactor
	if math.IsNaN(actualAmount) || math.IsInf(actualAmount, 0) {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("calculated amount is not finite: %f", actualAmount))
	}
	if actualAmount < 0 {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("calculated amount is negative: %f", actualAmount))
	}

	vaultLogger.Debug().
		Str("rawAmount", rawAmountStr).
		Float64("precisionFactor", precisionFactor).
		Float64("actualAmount", actualAmount).
		Msg("Successfully calculated USDC amount")

	return actualAmount, nil
}

// GetPoolPositions fetches pool positions with comprehensive validation
func (v *VaultClient) GetPoolPositions() ([]types.Position, error) {
	// Validate client state
	if err := v.validateClientState(); err != nil {
		return nil, err
	}

	// Ensure connection
	if err := v.ensureConnection(); err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to ensure gRPC connection for GetPoolPositions")
		return nil, errors.Join(ErrConnectionFailed, fmt.Errorf("failed to ensure gRPC connection: %w", err))
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ctx == nil {
		return nil, errors.New("failed to create timeout context")
	}

	// Query positions
	positions, err := v.queryClient.VaultPositions(ctx, &vaulttypes.QueryVaultPositionsRequest{
		VaultId: v.vaultId,
	})
	if err != nil {
		return nil, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to query vault positions: %w", err))
	}

	// Validate response
	if err := validatePositionsResponse(positions); err != nil {
		return nil, errors.Join(ErrInvalidResponse, err)
	}

	// Process pool positions with validation
	poolPositions := make([]types.Position, 0, len(positions.Positions))

	for i, position := range positions.Positions {
		if strings.HasPrefix(position.TokenDenom, "amm/pool") {
			poolPos, err := v.processPoolPosition(position, i)
			if err != nil {
				return nil, fmt.Errorf("failed to process pool position %d: %w", i, err)
			}
			poolPositions = append(poolPositions, poolPos)
		}
	}

	vaultLogger.Info().
		Int("poolPositionsCount", len(poolPositions)).
		Uint64("vaultId", v.vaultId).
		Msg("Retrieved pool positions from vault with validation")

	return poolPositions, nil
}

// processPoolPosition processes a single pool position with validation
func (v *VaultClient) processPoolPosition(position vaulttypes.PositionToken, index int) (types.Position, error) {
	// Extract and validate pool ID
	poolId, err := extractPoolID(position.TokenDenom)
	if err != nil {
		return types.Position{}, fmt.Errorf("position %d: %w", index, err)
	}

	// Validate LP shares
	lpShares := position.TokenAmount.TruncateInt()
	if lpShares.IsNil() || lpShares.IsZero() || lpShares.IsNegative() {
		return types.Position{}, fmt.Errorf("position %d has invalid LP shares", index)
	}

	// Validate estimated value
	estimatedValue := position.TokenUsdValue.MustFloat64()
	if math.IsNaN(estimatedValue) || math.IsInf(estimatedValue, 0) {
		return types.Position{}, fmt.Errorf("position %d has invalid estimated value: %f", index, estimatedValue)
	}
	if estimatedValue < 0 {
		return types.Position{}, fmt.Errorf("position %d has negative estimated value: %f", index, estimatedValue)
	}

	poolPosition := types.Position{
		PoolID:         types.PoolID(poolId),
		LPShares:       lpShares,
		AgeDays:        0, // Could be calculated if needed
		EstimatedValue: estimatedValue,
	}

	vaultLogger.Debug().
		Uint64("poolId", poolId).
		Str("lpShares", poolPosition.LPShares.String()).
		Float64("estimatedValue", poolPosition.EstimatedValue).
		Msg("Processed pool position with validation")

	return poolPosition, nil
}

// extractPoolID extracts pool ID from denom with validation
func extractPoolID(denom string) (uint64, error) {
	if denom == "" {
		return 0, errors.New("denom is empty")
	}
	if !strings.HasPrefix(denom, "amm/pool/") {
		return 0, fmt.Errorf("invalid pool denom format: %s", denom)
	}

	parts := strings.Split(denom, "/")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid pool denom structure: %s", denom)
	}

	poolId, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse pool ID from denom %s: %w", denom, err)
	}

	if poolId == 0 {
		return 0, fmt.Errorf("pool ID cannot be zero: %s", denom)
	}

	return poolId, nil
}

// GetNonPoolPositions fetches non-pool positions with comprehensive validation
func (v *VaultClient) GetNonPoolPositions() ([]types.TokenPosition, error) {
	// Validate client state
	if err := v.validateClientState(); err != nil {
		return nil, err
	}

	// Ensure connection
	if err := v.ensureConnection(); err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to ensure gRPC connection for GetNonPoolPositions")
		return nil, errors.Join(ErrConnectionFailed, fmt.Errorf("failed to ensure gRPC connection: %w", err))
	}

	// Create timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ctx == nil {
		return nil, errors.New("failed to create timeout context")
	}

	// Query positions
	positions, err := v.queryClient.VaultPositions(ctx, &vaulttypes.QueryVaultPositionsRequest{
		VaultId: v.vaultId,
	})
	if err != nil {
		return nil, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to query vault positions: %w", err))
	}

	// Validate response
	if err := validatePositionsResponse(positions); err != nil {
		return nil, errors.Join(ErrInvalidResponse, err)
	}

	// Find USDC token for filtering
	usdcToken, err := v.findAndValidateUSDCToken()
	if err != nil {
		return nil, err
	}

	// Process non-pool positions
	nonPoolPositions := make([]types.TokenPosition, 0)

	for i, position := range positions.Positions {
		// Skip LP tokens and USDC positions
		if strings.HasPrefix(position.TokenDenom, "amm/pool") ||
			position.TokenDenom == usdcToken.IBCDenom {
			continue
		}

		tokenPos, err := v.processTokenPosition(position, i)
		if err != nil {
			return nil, fmt.Errorf("failed to process token position %d: %w", i, err)
		}

		nonPoolPositions = append(nonPoolPositions, tokenPos)
	}

	vaultLogger.Debug().
		Int("nonPoolPositionsCount", len(nonPoolPositions)).
		Msg("Retrieved non-pool token positions with validation")

	return nonPoolPositions, nil
}

// processTokenPosition processes a single token position with validation
func (v *VaultClient) processTokenPosition(position vaulttypes.PositionToken, index int) (types.TokenPosition, error) {
	// Validate token amount
	amount := position.TokenAmount.TruncateInt()
	if amount.IsNil() || amount.IsNegative() {
		return types.TokenPosition{}, fmt.Errorf("position %d has invalid token amount", index)
	}

	// Validate estimated value
	estimatedValue := position.TokenUsdValue.MustFloat64()
	if math.IsNaN(estimatedValue) || math.IsInf(estimatedValue, 0) {
		return types.TokenPosition{}, fmt.Errorf("position %d has invalid estimated value: %f", index, estimatedValue)
	}
	if estimatedValue < 0 {
		return types.TokenPosition{}, fmt.Errorf("position %d has negative estimated value: %f", index, estimatedValue)
	}

	return types.TokenPosition{
		Denom:          position.TokenDenom,
		Amount:         amount,
		EstimatedValue: estimatedValue,
	}, nil
}

// GetTotalVaultValue fetches total vault value with comprehensive RPC validation
func (v *VaultClient) GetTotalVaultValue() (float64, error) {
	// Validate client state
	if err := v.validateClientState(); err != nil {
		return 0, err
	}

	// Validate RPC configuration
	if err := validateRPCConfig(); err != nil {
		return 0, errors.Join(ErrRPCRequestFailed, err)
	}

	// Create and marshal the gRPC request with validation
	grpcRequest := &vaulttypes.QueryVaultValue{
		VaultId: v.vaultId,
	}

	protoBytes, err := proto.Marshal(grpcRequest)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to marshal vault value gRPC request")
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to marshal gRPC request: %w", err))
	}

	if len(protoBytes) == 0 {
		return 0, errors.Join(ErrRPCRequestFailed, errors.New("marshaled request is empty"))
	}

	// Execute RPC call with comprehensive validation
	return v.executeVaultValueRPC(protoBytes)
}

// validateRPCConfig validates RPC configuration
func validateRPCConfig() error {
	if config.NodeRPC == "" {
		return errors.New("node RPC endpoint is not configured")
	}
	// Additional RPC configuration validation could be added here
	return nil
}

// executeVaultValueRPC executes the RPC call with comprehensive error handling
func (v *VaultClient) executeVaultValueRPC(protoBytes []byte) (float64, error) {
	hexEncodedData := hex.EncodeToString(protoBytes)
	if hexEncodedData == "" {
		return 0, errors.Join(ErrRPCRequestFailed, errors.New("hex encoding failed"))
	}

	// Construct JSON-RPC request with validation
	abciPath := "/elys.vaults.Query/VaultValue"
	jsonRPCReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "abci_query",
		Params: ABCIQueryParams{
			Path: abciPath,
			Data: hexEncodedData,
		},
	}

	// Marshal JSON request
	jsonData, err := json.Marshal(jsonRPCReq)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to marshal JSON-RPC request")
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to marshal JSON-RPC request: %w", err))
	}

	if len(jsonData) == 0 {
		return 0, errors.Join(ErrRPCRequestFailed, errors.New("JSON request is empty"))
	}

	// Execute HTTP request with timeout and validation
	return v.executeHTTPRequest(jsonData, abciPath, hexEncodedData)
}

// executeHTTPRequest executes the HTTP request with comprehensive validation
func (v *VaultClient) executeHTTPRequest(jsonData []byte, abciPath, hexData string) (float64, error) {
	// Create HTTP client with timeout
	httpClient := http.Client{
		Timeout: 20 * time.Second,
	}

	// Create request with validation
	req, err := http.NewRequest("POST", config.NodeRPC, bytes.NewBuffer(jsonData))
	if err != nil {
		vaultLogger.Error().Err(err).Str("endpoint", config.NodeRPC).Msg("Failed to create HTTP request")
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to create HTTP request: %w", err))
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	vaultLogger.Debug().
		Str("endpoint", config.NodeRPC).
		Str("abciPath", abciPath).
		Uint64("vaultId", v.vaultId).
		Msg("Executing RPC call for vault value")

	// Execute request
	resp, err := httpClient.Do(req)
	if err != nil {
		vaultLogger.Error().Err(err).Str("endpoint", config.NodeRPC).Msg("Failed to execute HTTP request")
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to execute HTTP request: %w", err))
	}
	defer resp.Body.Close()

	// Validate response status
	if resp.StatusCode != http.StatusOK {
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("HTTP request failed with status: %d %s", resp.StatusCode, resp.Status))
	}

	// Read and validate response body
	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to read RPC response body")
		return 0, errors.Join(ErrRPCRequestFailed, fmt.Errorf("failed to read response body: %w", err))
	}

	if len(respBodyBytes) == 0 {
		return 0, errors.Join(ErrInvalidResponse, errors.New("response body is empty"))
	}

	// Parse and validate JSON-RPC response
	return v.parseVaultValueResponse(respBodyBytes)
}

// parseVaultValueResponse parses and validates the vault value response
func (v *VaultClient) parseVaultValueResponse(respBodyBytes []byte) (float64, error) {
	var jsonRPCResp JSONRPCResponse
	if err := json.Unmarshal(respBodyBytes, &jsonRPCResp); err != nil {
		vaultLogger.Error().Err(err).Str("body", string(respBodyBytes)).Msg("Failed to unmarshal JSON-RPC response")
		return 0, errors.Join(ErrInvalidResponse, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err))
	}

	// Validate JSON-RPC response
	if err := validateJSONRPCResponse(jsonRPCResp); err != nil {
		return 0, errors.Join(ErrInvalidResponse, err)
	}

	// Decode and unmarshal protobuf response
	decodedValueBytes, err := base64.StdEncoding.DecodeString(jsonRPCResp.Result.Response.Value)
	if err != nil {
		vaultLogger.Error().Err(err).Str("base64_value", jsonRPCResp.Result.Response.Value).Msg("Failed to decode response value")
		return 0, errors.Join(ErrInvalidResponse, fmt.Errorf("failed to decode response value: %w", err))
	}

	if len(decodedValueBytes) == 0 {
		return 0, errors.Join(ErrInvalidResponse, errors.New("decoded response is empty"))
	}

	var grpcResponse vaulttypes.QueryVaultValueResponse
	if err := proto.Unmarshal(decodedValueBytes, &grpcResponse); err != nil {
		vaultLogger.Error().Err(err).Msg("Failed to unmarshal protobuf response")
		return 0, errors.Join(ErrInvalidResponse, fmt.Errorf("failed to unmarshal protobuf response: %w", err))
	}

	// Validate and extract USD value
	if grpcResponse.UsdValue.IsNil() {
		return 0, errors.Join(ErrInvalidResponse, errors.New("USD value is nil"))
	}

	usdValue := grpcResponse.UsdValue.MustFloat64()
	if math.IsNaN(usdValue) || math.IsInf(usdValue, 0) {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("USD value is not finite: %f", usdValue))
	}
	if usdValue < 0 {
		return 0, errors.Join(ErrMathematicalError, fmt.Errorf("USD value is negative: %f", usdValue))
	}

	vaultLogger.Info().
		Uint64("vaultId", v.vaultId).
		Float64("usdValue", usdValue).
		Msg("Successfully fetched vault value with validation")

	return usdValue, nil
}

// validateJSONRPCResponse validates the JSON-RPC response structure
func validateJSONRPCResponse(resp JSONRPCResponse) error {
	// Check for RPC errors
	if resp.Error != nil {
		return fmt.Errorf("RPC error (code %d): %s", resp.Error.Code, resp.Error.Message)
	}

	// Check ABCI response code
	if resp.Result.Response.Code != 0 {
		return fmt.Errorf("ABCI error (code %d): %s", resp.Result.Response.Code, resp.Result.Response.Log)
	}

	// Validate response value
	if resp.Result.Response.Value == "" {
		return errors.New("response value is empty")
	}

	return nil
}

// connect establishes gRPC connection with validation
func (v *VaultClient) connect() error {
	if config.NodeRPC == "" {
		return errors.Join(ErrConnectionFailed, errors.New("gRPC endpoint is empty"))
	}

	vaultLogger.Info().Str("endpoint", config.NodeRPC).Msg("Establishing gRPC connection")

	conn, err := grpc.NewClient(
		config.NodeRPC,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		vaultLogger.Error().Err(err).Str("endpoint", config.NodeRPC).Msg("Failed to connect to gRPC endpoint")
		return errors.Join(ErrConnectionFailed, fmt.Errorf("failed to connect to gRPC endpoint %s: %w", config.NodeRPC, err))
	}

	if conn == nil {
		return errors.Join(ErrConnectionFailed, errors.New("gRPC connection is nil"))
	}

	v.grpcConn = conn
	v.queryClient = vaulttypes.NewQueryClient(conn)

	if v.queryClient == nil {
		return errors.Join(ErrConnectionFailed, errors.New("failed to create query client"))
	}

	vaultLogger.Info().Str("endpoint", config.NodeRPC).Msg("gRPC connection established successfully")
	return nil
}

// Close closes the vault client with proper cleanup and validation
func (v *VaultClient) Close() error {
	if v == nil {
		return errors.New("vault client is nil")
	}

	vaultLogger.Info().Uint64("vaultId", v.vaultId).Msg("Closing VaultClient")

	// Cancel context
	if v.cancel != nil {
		v.cancel()
	}

	// Close gRPC connection
	if v.grpcConn != nil {
		err := v.grpcConn.Close()
		if err != nil {
			vaultLogger.Error().Err(err).Msg("Error closing gRPC connection")
			return fmt.Errorf("failed to close gRPC connection: %w", err)
		}
	}

	vaultLogger.Debug().Uint64("vaultId", v.vaultId).Msg("VaultClient closed successfully")
	return nil
}

// isConnected checks if the gRPC connection is valid with comprehensive validation
func (v *VaultClient) isConnected() bool {
	if v == nil || v.grpcConn == nil {
		return false
	}

	state := v.grpcConn.GetState()
	return state != connectivity.TransientFailure && state != connectivity.Shutdown
}

// ensureConnection ensures we have a valid gRPC connection with zero tolerance
func (v *VaultClient) ensureConnection() error {
	if v == nil {
		return errors.New("vault client is nil")
	}

	if !v.isConnected() {
		vaultLogger.Error().Msg("gRPC connection is invalid")
		return errors.Join(ErrConnectionFailed, errors.New("gRPC connection is not valid"))
	}

	return nil
}

// ExecuteActionPlan executes a list of SubActions and returns transaction details
func (v *VaultClient) ExecuteActionPlan(subActions []types.SubAction) (*types.TransactionResult, error) {
	vaultLogger.Info().
		Int("actionCount", len(subActions)).
		Uint64("vaultId", v.vaultId).
		Msg("ExecuteActionPlan: Starting action plan execution")

	// Validate inputs with zero tolerance
	if err := v.validateActionPlanInputs(subActions); err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Input validation failed")
		return nil, fmt.Errorf("action plan input validation failed: %w", err)
	}

	vaultLogger.Info().Msg("ExecuteActionPlan: Input validation passed")

	// Ensure connection is active
	if err := v.ensureConnection(); err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Connection validation failed")
		return nil, fmt.Errorf("vault connection validation failed: %w", err)
	}

	vaultLogger.Info().Msg("ExecuteActionPlan: Connection validated")

	// Create signing client with comprehensive validation
	signingClient, err := wallet.NewSigningClient(v.grpcConn)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Failed to create signing client")
		return nil, fmt.Errorf("failed to create signing client: %w", err)
	}

	vaultLogger.Info().Msg("ExecuteActionPlan: Signing client created successfully")

	// Create transaction builder with comprehensive validation
	txBuilder := wallet.NewTransactionBuilder(signingClient)

	vaultLogger.Info().Msg("ExecuteActionPlan: Transaction builder created successfully")

	// Process SubActions with comprehensive error handling
	vaultLogger.Info().Msg("ExecuteActionPlan: Processing SubActions...")
	txResponse, err := txBuilder.ProcessSubActions(subActions, v.vaultId)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Failed to process SubActions")
		return &types.TransactionResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, fmt.Errorf("failed to process SubActions: %w", err)
	}

	vaultLogger.Info().
		Str("txHash", txResponse.TxHash).
		Msg("ExecuteActionPlan: SubActions processed successfully")

	// Validate transaction response
	if err := v.validateTransactionResponse(txResponse); err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Transaction response validation failed")
		return &types.TransactionResult{
			TxHash:       txResponse.TxHash,
			Success:      false,
			ErrorMessage: err.Error(),
		}, fmt.Errorf("transaction response validation failed: %w", err)
	}

	vaultLogger.Info().
		Str("txHash", txResponse.TxHash).
		Msg("ExecuteActionPlan: Transaction response validated")

	// Wait for transaction inclusion to get complete details
	vaultLogger.Info().Msg("ExecuteActionPlan: Waiting for transaction inclusion...")
	completeTxResponse, err := v.waitForTransactionInclusion(signingClient, txResponse.TxHash)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Failed to get complete transaction details")
		return &types.TransactionResult{
			TxHash:       txResponse.TxHash,
			Success:      false,
			ErrorMessage: err.Error(),
		}, fmt.Errorf("failed to get complete transaction details: %w", err)
	}

	vaultLogger.Info().
		Str("txHash", completeTxResponse.TxHash).
		Int64("gasUsed", completeTxResponse.GasUsed).
		Int64("gasWanted", completeTxResponse.GasWanted).
		Msg("ExecuteActionPlan: Complete transaction details retrieved")

	// Extract gas fee from complete transaction
	gasFeeUSD, err := v.extractGasFeeFromResponse(completeTxResponse)
	if err != nil {
		vaultLogger.Error().Err(err).Msg("ExecuteActionPlan: Failed to extract gas fee")
		// Don't fail the transaction for gas fee extraction errors, just log and continue
		gasFeeUSD = 0.0
	}

	// Create transaction result
	result := &types.TransactionResult{
		TxHash:    completeTxResponse.TxHash,
		GasUsed:   completeTxResponse.GasUsed,
		GasWanted: completeTxResponse.GasWanted,
		GasFeeUSD: gasFeeUSD,
		Success:   true,
	}

	vaultLogger.Info().
		Str("txHash", result.TxHash).
		Int64("gasUsed", result.GasUsed).
		Int64("gasWanted", result.GasWanted).
		Float64("gasFeeUSD", result.GasFeeUSD).
		Msg("ExecuteActionPlan: Action plan executed successfully")

	return result, nil
}

// waitForTransactionInclusion waits for a transaction to be included in a block and returns complete data
func (v *VaultClient) waitForTransactionInclusion(signingClient *wallet.SigningClient, txHash string) (*sdk.TxResponse, error) {
	if txHash == "" {
		return nil, errors.New("transaction hash cannot be empty")
	}

	vaultLogger.Info().
		Str("txHash", txHash).
		Msg("Waiting for transaction to be included in block...")

	// Wait with exponential backoff and timeout
	maxAttempts := 30            // Maximum number of attempts
	baseDelay := 2 * time.Second // Base delay between attempts
	maxDelay := 30 * time.Second // Maximum delay between attempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Calculate delay with exponential backoff
		delay := time.Duration(float64(baseDelay) * math.Pow(1.5, float64(attempt-1)))
		if delay > maxDelay {
			delay = maxDelay
		}

		vaultLogger.Debug().
			Str("txHash", txHash).
			Int("attempt", attempt).
			Dur("delay", delay).
			Msg("Attempting to query transaction")

		// Wait before querying
		time.Sleep(delay)

		// Create context with timeout for the query
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Query the transaction
		txResponse, err := signingClient.QueryTxByHash(ctx, txHash)

		if err != nil {
			vaultLogger.Debug().
				Err(err).
				Str("txHash", txHash).
				Int("attempt", attempt).
				Msg("Transaction not yet available, will retry")

			// Continue to next attempt
			continue
		}

		// Validate the response has complete data
		if txResponse != nil && txResponse.GasUsed > 0 && len(txResponse.Events) > 0 {
			vaultLogger.Info().
				Str("txHash", txHash).
				Int("attempt", attempt).
				Int64("gasUsed", txResponse.GasUsed).
				Int("eventCount", len(txResponse.Events)).
				Msg("Transaction found in block with complete data")

			return txResponse, nil
		}

		vaultLogger.Debug().
			Str("txHash", txHash).
			Int("attempt", attempt).
			Msg("Transaction found but incomplete data, will retry")
	}

	// If we get here, we've exhausted all attempts
	return nil, fmt.Errorf("transaction %s was not found in block after %d attempts", txHash, maxAttempts)
}

// validateActionPlanInputs validates action plan inputs with zero tolerance
func (v *VaultClient) validateActionPlanInputs(subActions []types.SubAction) error {
	// Validate client state
	if err := v.validateClientState(); err != nil {
		return err
	}

	// Validate sub actions
	if subActions == nil {
		return errors.Join(ErrActionPlanInvalid, errors.New("sub actions cannot be nil"))
	}
	if len(subActions) == 0 {
		vaultLogger.Warn().Msg("No sub-actions provided for execution")
		return errors.Join(ErrActionPlanInvalid, errors.New("no sub-actions provided for execution"))
	}

	// Validate each sub action (basic validation - detailed validation happens in wallet)
	for i, action := range subActions {
		if err := v.validateSubAction(action, i); err != nil {
			return errors.Join(ErrActionPlanInvalid, fmt.Errorf("sub action %d validation failed: %w", i, err))
		}
	}

	return nil
}

// validateSubAction performs basic validation of a sub action
func (v *VaultClient) validateSubAction(action types.SubAction, index int) error {
	if action.Type == "" {
		return fmt.Errorf("action %d has empty type", index)
	}

	// Type-specific basic validation
	switch action.Type {
	case types.SubActionSwap:
		if action.TokenIn.Denom == "" || action.TokenOutDenom == "" {
			return fmt.Errorf("swap action %d has empty denominations", index)
		}
	case types.SubActionDepositLP:
		if action.PoolIDToDeposit == 0 {
			return fmt.Errorf("deposit action %d has zero pool ID", index)
		}
	case types.SubActionWithdrawLP:
		if action.PoolIDToWithdraw == 0 {
			return fmt.Errorf("withdraw action %d has zero pool ID", index)
		}
	default:
		return fmt.Errorf("action %d has unknown type: %s", index, action.Type)
	}

	return nil
}

// validateTransactionResponse validates the transaction response
func (v *VaultClient) validateTransactionResponse(txResponse *sdk.TxResponse) error {
	if txResponse == nil {
		return errors.New("transaction response is nil")
	}
	if txResponse.TxHash == "" {
		return errors.New("transaction hash is empty")
	}
	if txResponse.Code != 0 {
		return fmt.Errorf("transaction failed with code %d: %s", txResponse.Code, txResponse.RawLog)
	}
	return nil
}

// extractGasFeeFromResponse extracts gas fee from transaction response
func (v *VaultClient) extractGasFeeFromResponse(txResponse *sdk.TxResponse) (float64, error) {
	if txResponse == nil {
		return 0.0, errors.New("transaction response cannot be nil")
	}

	// Find ELYS token - REQUIRED for USD conversion
	var elysToken *types.Token
	for _, token := range v.Tokens {
		if token.Denom == "uelys" {
			tokenCopy := token
			elysToken = &tokenCopy
			break
		}
	}

	if elysToken == nil {
		return 0.0, errors.New("ELYS token not found in token data - cannot convert gas fees to USD")
	}

	// Validate ELYS price
	if elysToken.PriceUSD <= 0 {
		return 0.0, fmt.Errorf("invalid ELYS price: %f", elysToken.PriceUSD)
	}

	// Log transaction details for debugging
	vaultLogger.Debug().
		Str("txHash", txResponse.TxHash).
		Int64("gasUsed", txResponse.GasUsed).
		Int64("gasWanted", txResponse.GasWanted).
		Int("eventCount", len(txResponse.Events)).
		Msg("Extracting gas fee from complete transaction response")

	var totalFeeInUelys float64

	// Extract fee from transaction events - only use actual event data
	for _, event := range txResponse.Events {
		// Check for "tx" type events (standard fee location)
		if event.Type == "tx" {
			for _, attr := range event.Attributes {
				if attr.Key == "fee" {
					if feeAmount := v.extractElysFeeFromString(attr.Value); feeAmount > 0 {
						totalFeeInUelys += feeAmount
						vaultLogger.Debug().
							Str("source", "tx.fee").
							Float64("amount", feeAmount).
							Msg("Found fee in tx event")
					}
				}
			}
		}
	}

	// If no fee found in events, this is an error for a system managing millions
	if totalFeeInUelys == 0 {
		return 0.0, errors.New("no ELYS gas fee found in transaction events - cannot calculate costs accurately")
	}

	// Convert uelys to ELYS using the token's actual precision factor
	precisionFactor := math.Pow10(elysToken.Precision)
	gasFeeInElys := totalFeeInUelys / precisionFactor
	gasFeeUSD := gasFeeInElys * elysToken.PriceUSD

	// Validate calculated gas fee
	if gasFeeUSD < 0 {
		return 0.0, fmt.Errorf("calculated negative gas fee: %f", gasFeeUSD)
	}

	vaultLogger.Info().
		Float64("totalFeeUelys", totalFeeInUelys).
		Float64("precisionFactor", precisionFactor).
		Float64("gasFeeElys", gasFeeInElys).
		Float64("gasFeeUSD", gasFeeUSD).
		Float64("elysPriceUSD", elysToken.PriceUSD).
		Str("txHash", txResponse.TxHash).
		Msg("Successfully extracted gas fee from complete transaction")

	return gasFeeUSD, nil
}

// extractElysFeeFromString extracts ELYS fee amount from a coin string
func (v *VaultClient) extractElysFeeFromString(coinStr string) float64 {
	// Handle multiple coins (e.g., "1000uelys,500uatom")
	coins := strings.Split(coinStr, ",")
	var totalUelys float64

	for _, coin := range coins {
		coin = strings.TrimSpace(coin)
		if strings.HasSuffix(coin, "uelys") {
			// Remove "uelys" suffix and parse the amount
			amountStr := strings.TrimSuffix(coin, "uelys")
			if amount, err := strconv.ParseFloat(amountStr, 64); err == nil && amount > 0 {
				totalUelys += amount
			}
		}
	}

	return totalUelys
}

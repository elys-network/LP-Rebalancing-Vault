package simulations

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/logger"
	amm "github.com/elys-network/elys/v6/x/amm/types"
	"github.com/gogo/protobuf/proto"
	"github.com/rs/zerolog"
)

const (
	rpcTimeout = 20 * time.Second
)

var (
	swapLogger     = logger.GetForComponent("swap_simulator")
	joinPoolLogger = logger.GetForComponent("join_pool_simulator")
	exitPoolLogger = logger.GetForComponent("exit_pool_simulator")
)

// --- Shared JSON-RPC Structures ---

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

// --- Result Types ---

// SwapEstimationResult contains the result of a swap simulation
type SwapEstimationResult struct {
	TokenOutAmount math.Int
	Slippage       float64
}

// JoinPoolEstimationResult contains the result of a join pool simulation
type JoinPoolEstimationResult struct {
	ShareAmountOut            sdk.Coin   // LP shares received
	AmountsIn                 []sdk.Coin // Actual amounts that will be deposited
	Slippage                  float64
	WeightBalanceRatio        float64
	SwapFee                   float64
	TakerFee                  float64
	WeightBalanceRewardAmount sdk.Coin
}

// ExitPoolEstimationResult contains the result of an exit pool simulation
type ExitPoolEstimationResult struct {
	AmountsOut                []sdk.Coin // Tokens received from exiting
	WeightBalanceRatio        float64
	Slippage                  float64
	SwapFee                   float64
	TakerFee                  float64
	WeightBalanceRewardAmount sdk.Coin
}

// --- Simulation Functions ---

// SimulateSwap simulates a token swap using Tendermint RPC
func SimulateSwap(
	_ any, // Unused parameter for compatibility
	tokenInAmount math.Int,
	tokenInDenom, tokenOutDenom string,
) (SwapEstimationResult, error) {
	return simulateSwapWithEndpoint(config.NodeRPC, tokenInAmount, tokenInDenom, tokenOutDenom)
}

// simulateSwapWithEndpoint performs the actual swap simulation
func simulateSwapWithEndpoint(
	rpcEndpoint string,
	tokenInAmount math.Int,
	tokenInDenom, tokenOutDenom string,
) (SwapEstimationResult, error) {
	// Hardcoded address as per original implementation
	address := "elys1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgp"

	// Create and Marshal the gRPC Request Payload
	grpcRequest := &amm.QuerySwapEstimationByDenomRequest{
		DenomIn:  tokenInDenom,
		DenomOut: tokenOutDenom,
		Amount: sdk.Coin{
			Denom:  tokenInDenom,
			Amount: tokenInAmount,
		},
		Address: address,
	}

	result, err := executeRPCQuery(
		rpcEndpoint,
		"/elys.amm.Query/SwapEstimationByDenom",
		grpcRequest,
		swapLogger,
		1, // RPC ID
	)
	if err != nil {
		return SwapEstimationResult{}, err
	}

	// Unmarshal the response
	var grpcResponse amm.QuerySwapEstimationByDenomResponse
	if err := proto.Unmarshal(result, &grpcResponse); err != nil {
		swapLogger.Error().Err(err).Msg("Failed to unmarshal swap response")
		return SwapEstimationResult{}, fmt.Errorf("failed to unmarshal swap response: %w", err)
	}

	// Parse slippage
	slippageFloat, err := strconv.ParseFloat(grpcResponse.Slippage.String(), 64)
	if err != nil {
		swapLogger.Error().Err(err).Str("slippage", grpcResponse.Slippage.String()).Msg("Failed to parse slippage")
		slippageFloat = 0.0
	}

	swapLogger.Info().
		Str("tokenIn", fmt.Sprintf("%s%s", tokenInAmount.String(), tokenInDenom)).
		Str("tokenOut", fmt.Sprintf("%s%s", grpcResponse.Amount.Amount.String(), grpcResponse.Amount.Denom)).
		Float64("slippage", slippageFloat).
		Msg("Swap simulation completed")

	return SwapEstimationResult{
		TokenOutAmount: grpcResponse.Amount.Amount,
		Slippage:       slippageFloat,
	}, nil
}

// SimulateJoinPool simulates joining a liquidity pool
func SimulateJoinPool(
	rpcEndpoint string,
	poolId uint64,
	amountsIn []sdk.Coin,
) (JoinPoolEstimationResult, error) {
	// Convert types.Coin to sdk.Coin
	sdkCoins := make([]sdk.Coin, len(amountsIn))
	for i, coin := range amountsIn {
		sdkCoins[i] = sdk.Coin{
			Denom:  coin.Denom,
			Amount: coin.Amount,
		}
	}

	// Create gRPC request
	grpcRequest := &amm.QueryJoinPoolEstimationRequest{
		PoolId:    poolId,
		AmountsIn: sdkCoins,
	}

	result, err := executeRPCQuery(
		rpcEndpoint,
		"/elys.amm.Query/JoinPoolEstimation",
		grpcRequest,
		joinPoolLogger,
		2, // RPC ID
	)
	if err != nil {
		return JoinPoolEstimationResult{}, err
	}

	// Unmarshal the response
	var grpcResponse amm.QueryJoinPoolEstimationResponse
	if err := proto.Unmarshal(result, &grpcResponse); err != nil {
		joinPoolLogger.Error().Err(err).Msg("Failed to unmarshal join pool response")
		return JoinPoolEstimationResult{}, fmt.Errorf("failed to unmarshal join pool response: %w", err)
	}

	// Parse decimal fields
	slippage, _ := strconv.ParseFloat(grpcResponse.Slippage.String(), 64)
	weightBalanceRatio, _ := strconv.ParseFloat(grpcResponse.WeightBalanceRatio.String(), 64)
	swapFee, _ := strconv.ParseFloat(grpcResponse.SwapFee.String(), 64)
	takerFee, _ := strconv.ParseFloat(grpcResponse.TakerFee.String(), 64)

	// Convert sdk.Coin back to types.Coin
	amountsInResult := make([]sdk.Coin, len(grpcResponse.AmountsIn))
	for i, coin := range grpcResponse.AmountsIn {
		amountsInResult[i] = sdk.Coin{
			Denom:  coin.Denom,
			Amount: coin.Amount,
		}
	}

	joinPoolLogger.Info().
		Uint64("poolId", poolId).
		Float64("slippage", slippage).
		Str("shareAmountOut", grpcResponse.ShareAmountOut.String()).
		Msg("Join pool simulation completed")

	return JoinPoolEstimationResult{
		ShareAmountOut: sdk.Coin{
			Denom:  grpcResponse.ShareAmountOut.Denom,
			Amount: grpcResponse.ShareAmountOut.Amount,
		},
		AmountsIn:          amountsInResult,
		Slippage:           slippage,
		WeightBalanceRatio: weightBalanceRatio,
		SwapFee:            swapFee,
		TakerFee:           takerFee,
		WeightBalanceRewardAmount: sdk.Coin{
			Denom:  grpcResponse.WeightBalanceRewardAmount.Denom,
			Amount: grpcResponse.WeightBalanceRewardAmount.Amount,
		},
	}, nil
}

// SimulateLeavePool simulates exiting a liquidity pool
func SimulateLeavePool(
	rpcEndpoint string,
	poolId uint64,
	sharesIn math.Int,
	tokenOutDenom string, // Optional: specific token to exit to
) (ExitPoolEstimationResult, error) {
	// Create gRPC request
	grpcRequest := &amm.QueryExitPoolEstimationRequest{
		PoolId:        poolId,
		ShareAmountIn: sharesIn,
		TokenOutDenom: tokenOutDenom,
	}

	result, err := executeRPCQuery(
		rpcEndpoint,
		"/elys.amm.Query/ExitPoolEstimation",
		grpcRequest,
		exitPoolLogger,
		3, // RPC ID
	)
	if err != nil {
		return ExitPoolEstimationResult{}, err
	}

	// Unmarshal the response
	var grpcResponse amm.QueryExitPoolEstimationResponse
	if err := proto.Unmarshal(result, &grpcResponse); err != nil {
		exitPoolLogger.Error().Err(err).Msg("Failed to unmarshal exit pool response")
		return ExitPoolEstimationResult{}, fmt.Errorf("failed to unmarshal exit pool response: %w", err)
	}

	// Parse decimal fields
	slippage, _ := strconv.ParseFloat(grpcResponse.Slippage.String(), 64)
	weightBalanceRatio, _ := strconv.ParseFloat(grpcResponse.WeightBalanceRatio.String(), 64)
	swapFee, _ := strconv.ParseFloat(grpcResponse.SwapFee.String(), 64)
	takerFee, _ := strconv.ParseFloat(grpcResponse.TakerFee.String(), 64)

	// Convert sdk.Coin to types.Coin
	amountsOut := make([]sdk.Coin, len(grpcResponse.AmountsOut))
	for i, coin := range grpcResponse.AmountsOut {
		amountsOut[i] = sdk.Coin{
			Denom:  coin.Denom,
			Amount: coin.Amount,
		}
	}

	exitPoolLogger.Info().
		Uint64("poolId", poolId).
		Float64("slippage", slippage).
		Interface("amountsOut", amountsOut).
		Msg("Exit pool simulation completed")

	return ExitPoolEstimationResult{
		AmountsOut:         amountsOut,
		Slippage:           slippage,
		WeightBalanceRatio: weightBalanceRatio,
		SwapFee:            swapFee,
		TakerFee:           takerFee,
		WeightBalanceRewardAmount: sdk.Coin{
			Denom:  grpcResponse.WeightBalanceRewardAmount.Denom,
			Amount: grpcResponse.WeightBalanceRewardAmount.Amount,
		},
	}, nil
}

// --- Helper Functions ---

// executeRPCQuery executes a generic RPC query and returns the decoded result
func executeRPCQuery(
	rpcEndpoint string,
	abciPath string,
	grpcRequest proto.Message,
	logger zerolog.Logger,
	rpcID int,
) ([]byte, error) {
	// Marshal the gRPC request
	protoBytes, err := proto.Marshal(grpcRequest)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal gRPC request")
		return nil, fmt.Errorf("failed to marshal gRPC request: %w", err)
	}
	hexEncodedData := hex.EncodeToString(protoBytes)

	// Construct JSON-RPC request
	jsonRPCReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      rpcID,
		Method:  "abci_query",
		Params: ABCIQueryParams{
			Path: abciPath,
			Data: hexEncodedData,
		},
	}

	jsonData, err := json.Marshal(jsonRPCReq)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal JSON-RPC request")
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	logger.Debug().
		Str("endpoint", rpcEndpoint).
		Str("abciPath", abciPath).
		Msg("Executing RPC query")

	// Make HTTP request
	httpClient := http.Client{Timeout: rpcTimeout}
	req, err := http.NewRequest("POST", rpcEndpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create HTTP request")
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to send HTTP request")
		return nil, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse JSON-RPC response
	var jsonRPCResp JSONRPCResponse
	if err := json.Unmarshal(respBodyBytes, &jsonRPCResp); err != nil {
		logger.Error().Err(err).Str("body", string(respBodyBytes)).Msg("Failed to unmarshal JSON-RPC response")
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Check for RPC errors
	if jsonRPCResp.Error != nil {
		logger.Error().
			Int("code", jsonRPCResp.Error.Code).
			Str("message", jsonRPCResp.Error.Message).
			Msg("RPC error received")
		return nil, fmt.Errorf("RPC error: %s (code %d)", jsonRPCResp.Error.Message, jsonRPCResp.Error.Code)
	}

	// Check for ABCI errors
	if jsonRPCResp.Result.Response.Code != 0 {
		logger.Error().
			Uint32("code", jsonRPCResp.Result.Response.Code).
			Str("log", jsonRPCResp.Result.Response.Log).
			Msg("ABCI query error")
		return nil, fmt.Errorf("ABCI query error (code %d): %s", jsonRPCResp.Result.Response.Code, jsonRPCResp.Result.Response.Log)
	}

	if jsonRPCResp.Result.Response.Value == "" {
		logger.Warn().Str("log", jsonRPCResp.Result.Response.Log).Msg("Empty ABCI query result")
		return nil, fmt.Errorf("empty ABCI query result: %s", jsonRPCResp.Result.Response.Log)
	}

	// Decode base64 result
	decodedValueBytes, err := base64.StdEncoding.DecodeString(jsonRPCResp.Result.Response.Value)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to decode base64 result")
		return nil, fmt.Errorf("failed to decode base64 result: %w", err)
	}

	return decodedValueBytes, nil
}

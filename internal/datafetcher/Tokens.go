package datafetcher

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/elys-network/avm/internal/analyzer"
	"github.com/elys-network/avm/internal/logger"
	"google.golang.org/grpc"

	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/types"
	tier "github.com/elys-network/elys/v6/x/tier/types"

	assetprofiletypes "github.com/elys-network/elys/v6/x/assetprofile/types"
)

var tokenLogger = logger.GetForComponent("token_retriever")
var ErrInvalidTokenData = errors.New("invalid token data")
var ErrMissingRequiredData = errors.New("missing required token data")
var ErrInsufficientPriceData = errors.New("insufficient price data for financial calculations")

// GetTokens fetches all tokens on chain and returns them as a map keyed by denom
// Returns error if any token fails validation - no partial results with financial data
func GetTokens(grpcClient *grpc.ClientConn) (map[string]types.Token, error) {
	tokenLogger.Info().Msg("Starting strict token data retrieval")

	// Validate GRPC client
	if grpcClient == nil {
		return nil, errors.New("GRPC client cannot be nil")
	}

	// Fetch all token metadata with strict validation
	tokens, err := FetchAllTokens(grpcClient)
	if err != nil {
		tokenLogger.Error().Err(err).Msg("Failed to fetch token metadata")
		return nil, fmt.Errorf("token metadata fetch failed: %w", err)
	}

	if len(tokens) == 0 {
		tokenLogger.Error().Msg("No tokens returned from metadata fetch")
		return nil, errors.New("no token metadata available")
	}

	// Fetch all token prices with strict validation
	priceMap, err := FetchAllTokenPrices(grpcClient)
	if err != nil {
		tokenLogger.Error().Err(err).Msg("Failed to fetch token prices")
		return nil, fmt.Errorf("token price fetch failed: %w", err)
	}

	if len(priceMap) == 0 {
		tokenLogger.Error().Msg("No token prices available")
		return nil, errors.New("no token price data available")
	}

	// Initialize the token map
	tokenMap := make(map[string]types.Token)
	processedCount := 0

	tokenLogger.Info().
		Int("totalTokensFromAPI", len(tokens)).
		Msg("Starting token processing")

	for i, token := range tokens {
		tokenLogger.Debug().
			Int("tokenIndex", i).
			Str("denom", token.Denom).
			Str("displayName", token.DisplayName).
			Str("baseDenom", token.BaseDenom).
			Msg("Processing token")

		// Skip AMM pool denoms, stablestake denoms, and specific Eden denoms
		if strings.HasPrefix(token.Denom, "amm/") {
			tokenLogger.Debug().Str("denom", token.Denom).Msg("Skipping AMM pool denom")
			continue
		}
		if strings.HasPrefix(token.Denom, "stablestake/") {
			tokenLogger.Debug().Str("denom", token.Denom).Msg("Skipping stablestake denom")
			continue
		}
		if token.Denom == "uedenb" || token.Denom == "ueden" {
			tokenLogger.Debug().Str("denom", token.Denom).Msg("Skipping Eden-specific denom")
			continue
		}

		// Strict validation of token metadata
		if err := validateTokenMetadata(token); err != nil {
			tokenLogger.Error().
				Err(err).
				Str("denom", token.Denom).
				Str("displayName", token.DisplayName).
				Msg("Token metadata validation failed")
			return nil, fmt.Errorf("token metadata validation failed for %s: %w", token.DisplayName, err)
		}

		// Check if price data exists
		price, priceExists := priceMap[token.Denom]
		if !priceExists {
			tokenLogger.Warn().
				Str("denom", token.Denom).
				Str("displayName", token.DisplayName).
				Msg("No price data available for token - skipping")
			continue
		}

		// Strict validation of price data - skip tokens with invalid price data
		if err := validatePriceData(price, token.DisplayName); err != nil {
			tokenLogger.Warn().
				Err(err).
				Str("denom", token.Denom).
				Str("displayName", token.DisplayName).
				Msg("Price data validation failed - skipping token")
			continue
		}

		// Create token with validated metadata
		newToken := types.Token{
			Symbol:    strings.ToUpper(token.DisplayName),
			Denom:     token.BaseDenom,
			IBCDenom:  token.Denom,
			Precision: int(token.Decimals),
			PriceData: []types.PriceData{},
		}

		// Set price information (guaranteed to be valid from validation above)
		if price.OraclePrice.IsPositive() {
			priceFloat, err := price.OraclePrice.Float64()
			if err != nil {
				return nil, fmt.Errorf("oracle price conversion failed for %s: %w", token.DisplayName, err)
			}
			newToken.PriceUSD = priceFloat
			newToken.OracleSourced = true
		} else {
			priceFloat, err := price.AmmPrice.Float64()
			if err != nil {
				return nil, fmt.Errorf("AMM price conversion failed for %s: %w", token.DisplayName, err)
			}
			newToken.PriceUSD = priceFloat
			newToken.OracleSourced = false
		}

		// Check if symbol has CryptoCompare mapping, fallback to symbol itself
		ccSymbol := config.CoinToCCId[newToken.Symbol]
		if ccSymbol == "" {
			tokenLogger.Debug().
				Str("symbol", newToken.Symbol).
				Msg("Token symbol not found in CoinToCCId mapping, using symbol directly")
			ccSymbol = newToken.Symbol
		} else {
			tokenLogger.Info().
				Str("symbol", newToken.Symbol).
				Str("mappedSymbol", ccSymbol).
				Msg("Found CryptoCompare mapping for token")
		}

		// Fetch historical price data (required for volatility)
		tokenLogger.Info().
			Str("symbol", newToken.Symbol).
			Str("ccSymbol", ccSymbol).
			Str("denom", newToken.Denom).
			Float64("priceUSD", newToken.PriceUSD).
			Msg("Fetching historical price data for token")

		thirtyDayPrices, err := FetchHistoricalPriceData(ccSymbol)
		if err != nil {
			tokenLogger.Error().
				Err(err).
				Str("symbol", newToken.Symbol).
				Str("ccSymbol", ccSymbol).
				Msg("Failed to fetch historical price data")
			return nil, fmt.Errorf("historical price data fetch failed for %s: %w", ccSymbol, err)
		}

		newToken.PriceData = thirtyDayPrices

		// Calculate volatility (required for risk assessment)
		tokenLogger.Debug().
			Str("symbol", newToken.Symbol).
			Int("priceDataPoints", len(newToken.PriceData)).
			Msg("Calculating volatility")

		volatility, err := analyzer.CalculateVolatility(newToken.PriceData, 8760.0)
		if err != nil {
			tokenLogger.Error().
				Err(err).
				Str("symbol", newToken.Symbol).
				Msg("Failed to calculate volatility")
			return nil, fmt.Errorf("volatility calculation failed for %s: %w", newToken.Symbol, err)
		}

		newToken.Volatility = volatility

		// Final validation - ensure token is ready for financial use
		if err := validateTokenForFinancialUse(newToken); err != nil {
			tokenLogger.Error().
				Err(err).
				Str("symbol", newToken.Symbol).
				Msg("Final token validation failed")
			return nil, fmt.Errorf("final validation failed for %s: %w", newToken.Symbol, err)
		}

		// Add validated token to map
		tokenMap[newToken.Denom] = newToken

		// Also add an entry using IBCDenom as key if it's different
		if newToken.IBCDenom != newToken.Denom {
			tokenMap[newToken.IBCDenom] = newToken
		}

		processedCount++
		tokenLogger.Debug().
			Str("symbol", newToken.Symbol).
			Str("denom", newToken.Denom).
			Float64("priceUSD", newToken.PriceUSD).
			Float64("volatility", newToken.Volatility).
			Bool("oracleSourced", newToken.OracleSourced).
			Msg("Successfully processed and validated token")
	}

	if processedCount == 0 {
		tokenLogger.Error().Msg("No tokens were successfully processed")
		return nil, errors.New("no valid tokens found for financial calculations")
	}

	tokenLogger.Info().
		Int("totalTokens", len(tokens)).
		Int("processedTokens", processedCount).
		Int("mapEntries", len(tokenMap)).
		Msg("Successfully retrieved and validated all token data")

	return tokenMap, nil
}

func FetchAllTokens(grpcClient *grpc.ClientConn) ([]assetprofiletypes.Entry, error) {
	if grpcClient == nil {
		return nil, errors.New("GRPC client cannot be nil")
	}

	tokenLogger.Debug().Msg("Fetching all token entries from assetprofile module")
	assetProfileClient := assetprofiletypes.NewQueryClient(grpcClient)

	// Fetch all token entries using pagination to handle large datasets
	var allEntries []assetprofiletypes.Entry
	var nextKey []byte
	pageLimit := uint64(500) // Reasonable page size for token entries

	for {
		// Configure pagination for each request
		paginationReq := &query.PageRequest{
			Key:        nextKey,
			Limit:      pageLimit,
			CountTotal: false, // We don't need total count for this use case
		}

		response, err := assetProfileClient.EntryAll(
			context.Background(),
			&assetprofiletypes.QueryAllEntryRequest{
				Pagination: paginationReq,
			},
		)
		if err != nil {
			tokenLogger.Error().Err(err).Msg("Failed to fetch token entries from assetprofile module")
			return nil, fmt.Errorf("assetprofile query failed: %w", err)
		}

		if response == nil {
			tokenLogger.Error().Msg("Received nil response from assetprofile module")
			return nil, errors.New("nil response from assetprofile module")
		}

		// Append results from this page
		allEntries = append(allEntries, response.Entry...)

		// Check if there are more pages
		if response.Pagination == nil || response.Pagination.NextKey == nil || len(response.Pagination.NextKey) == 0 {
			break
		}

		nextKey = response.Pagination.NextKey
		tokenLogger.Debug().
			Int("fetchedEntries", len(response.Entry)).
			Int("totalEntriesSoFar", len(allEntries)).
			Msg("Fetched page of token entries, continuing pagination")
	}

	if len(allEntries) == 0 {
		tokenLogger.Error().Msg("No token entries returned from assetprofile module")
		return nil, errors.New("no token entries available from assetprofile module")
	}

	tokenLogger.Info().
		Int("tokenCount", len(allEntries)).
		Msg("Successfully fetched token entries from assetprofile")

	return allEntries, nil
}

// FetchAllTokenPrices fetches all token prices and returns them as a map keyed by denom
func FetchAllTokenPrices(grpcClient *grpc.ClientConn) (map[string]*tier.Price, error) {
	if grpcClient == nil {
		return nil, errors.New("GRPC client cannot be nil")
	}

	tokenLogger.Debug().Msg("Fetching token prices from tier module")
	tierClient := tier.NewQueryClient(grpcClient)

	// Fetch all token prices using pagination (note: tier module may not implement server-side pagination fully)
	var allPrices []*tier.Price
	var nextKey []byte
	pageLimit := uint64(500) // Reasonable page size for price entries

	for {
		// Configure pagination for each request
		paginationReq := &query.PageRequest{
			Key:        nextKey,
			Limit:      pageLimit,
			CountTotal: false, // We don't need total count for this use case
		}

		response, err := tierClient.GetAllPrices(
			context.Background(),
			&tier.QueryGetAllPricesRequest{
				Pagination: paginationReq,
			},
		)
		if err != nil {
			tokenLogger.Error().Err(err).Msg("Failed to fetch token prices from tier module")
			return nil, fmt.Errorf("tier module price query failed: %w", err)
		}

		if response == nil {
			tokenLogger.Error().Msg("Received nil response from tier module")
			return nil, errors.New("nil response from tier module")
		}

		// Append results from this page
		allPrices = append(allPrices, response.Prices...)

		// Check if there are more pages
		if response.Pagination == nil || response.Pagination.NextKey == nil || len(response.Pagination.NextKey) == 0 {
			break
		}

		nextKey = response.Pagination.NextKey
		tokenLogger.Debug().
			Int("fetchedPrices", len(response.Prices)).
			Int("totalPricesSoFar", len(allPrices)).
			Msg("Fetched page of token prices, continuing pagination")
	}

	if len(allPrices) == 0 {
		tokenLogger.Error().Msg("No token prices returned from tier module")
		return nil, errors.New("no token prices available from tier module")
	}

	// Create a map of prices keyed by denom
	priceMap := make(map[string]*tier.Price)
	validPriceCount := 0

	for _, price := range allPrices {
		if price == nil {
			tokenLogger.Error().Msg("Received nil price entry")
			return nil, errors.New("received nil price entry from tier module")
		}

		// Basic validation of price entry
		if strings.TrimSpace(price.Denom) == "" {
			tokenLogger.Error().Msg("Received price entry with empty denom")
			return nil, errors.New("received price entry with empty denom")
		}

		priceMap[price.Denom] = price
		validPriceCount++

		tokenLogger.Debug().
			Str("denom", price.Denom).
			Str("oraclePrice", price.OraclePrice.String()).
			Str("ammPrice", price.AmmPrice.String()).
			Msg("Token price retrieved")
	}

	if validPriceCount == 0 {
		tokenLogger.Error().Msg("No valid token prices found")
		return nil, errors.New("no valid token prices available")
	}

	tokenLogger.Info().
		Int("totalPrices", len(allPrices)).
		Int("validPrices", validPriceCount).
		Msg("Successfully retrieved and validated token prices")

	return priceMap, nil
}

// validateTokenMetadata performs strict validation on token metadata
func validateTokenMetadata(token assetprofiletypes.Entry) error {
	// Validate display name
	if strings.TrimSpace(token.DisplayName) == "" {
		return fmt.Errorf("token display name cannot be empty for denom %s", token.Denom)
	}

	// Validate base denom
	if strings.TrimSpace(token.BaseDenom) == "" {
		return fmt.Errorf("token base denom cannot be empty for %s", token.DisplayName)
	}

	// Validate IBC denom
	if strings.TrimSpace(token.Denom) == "" {
		return fmt.Errorf("token IBC denom cannot be empty for %s", token.DisplayName)
	}

	// Validate decimals/precision
	if token.Decimals == 0 {
		return fmt.Errorf("token decimals cannot be zero for %s (precision required for financial calculations)", token.DisplayName)
	}

	return nil
}

// validatePriceData performs strict validation on price data
func validatePriceData(price *tier.Price, tokenSymbol string) error {
	if price == nil {
		return fmt.Errorf("price data is nil for token %s", tokenSymbol)
	}

	// Validate denom exists
	if strings.TrimSpace(price.Denom) == "" {
		return fmt.Errorf("price denom is empty for token %s", tokenSymbol)
	}

	// At least one price source must be available and positive
	hasValidOraclePrice := price.OraclePrice.IsPositive()
	hasValidAmmPrice := price.AmmPrice.IsPositive()

	if !hasValidOraclePrice && !hasValidAmmPrice {
		return fmt.Errorf("no valid price available for token %s: oracle=%s, amm=%s",
			tokenSymbol, price.OraclePrice.String(), price.AmmPrice.String())
	}

	// Validate the price values are finite
	if hasValidOraclePrice {
		oracleFloat, err := price.OraclePrice.Float64()
		if err != nil {
			return fmt.Errorf("oracle price conversion failed for token %s: %w", tokenSymbol, err)
		}
		if math.IsNaN(oracleFloat) || math.IsInf(oracleFloat, 0) {
			return fmt.Errorf("oracle price is not finite for token %s: %f", tokenSymbol, oracleFloat)
		}
	}

	if hasValidAmmPrice {
		ammFloat, err := price.AmmPrice.Float64()
		if err != nil {
			return fmt.Errorf("AMM price conversion failed for token %s: %w", tokenSymbol, err)
		}
		if math.IsNaN(ammFloat) || math.IsInf(ammFloat, 0) {
			return fmt.Errorf("AMM price is not finite for token %s: %f", tokenSymbol, ammFloat)
		}
	}

	return nil
}

// validateTokenForFinancialUse ensures the token has all required data for financial calculations
func validateTokenForFinancialUse(token types.Token) error {
	// Validate basic fields
	if strings.TrimSpace(token.Symbol) == "" {
		return errors.New("token symbol cannot be empty")
	}
	if strings.TrimSpace(token.Denom) == "" {
		return errors.New("token denom cannot be empty")
	}
	if strings.TrimSpace(token.IBCDenom) == "" {
		return errors.New("token IBC denom cannot be empty")
	}

	// Validate precision
	if token.Precision <= 0 {
		return fmt.Errorf("token %s has invalid precision: %d", token.Symbol, token.Precision)
	}

	// Validate price
	if token.PriceUSD <= 0 {
		return fmt.Errorf("token %s has invalid USD price: %f", token.Symbol, token.PriceUSD)
	}
	if math.IsNaN(token.PriceUSD) || math.IsInf(token.PriceUSD, 0) {
		return fmt.Errorf("token %s USD price is not finite: %f", token.Symbol, token.PriceUSD)
	}

	// Validate volatility calculation requirements
	if token.Volatility < 0 {
		return fmt.Errorf("token %s has negative volatility: %f", token.Symbol, token.Volatility)
	}
	if math.IsNaN(token.Volatility) || math.IsInf(token.Volatility, 0) {
		return fmt.Errorf("token %s volatility is not finite: %f", token.Symbol, token.Volatility)
	}

	// For financial calculations, we need sufficient price data
	if len(token.PriceData) < 720 { // 30 days of hourly data
		return fmt.Errorf("token %s has insufficient price data for volatility calculation: %d points, 720 required",
			token.Symbol, len(token.PriceData))
	}

	return nil
}

package datafetcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types"
	amm "github.com/elys-network/elys/v6/x/amm/types"
	assetprofiletypes "github.com/elys-network/elys/v6/x/assetprofile/types"
	masterchef "github.com/elys-network/elys/v6/x/masterchef/types"
	"google.golang.org/grpc"
)

var poolLogger = logger.GetForComponent("pool_retriever")
var ErrInvalidPoolData = errors.New("invalid pool data")
var ErrMissingCriticalData = errors.New("missing critical pool data for financial calculations")
var ErrPoolValidationFailed = errors.New("pool validation failed")


// GetPools fetches pools for supported assets with strict validation - no partial results for financial calculations
func GetPools(grpcClient *grpc.ClientConn, supportedTokens []string) ([]types.Pool, error) {
	poolLogger.Info().Int("supportedTokenCount", len(supportedTokens)).Msg("Starting strict pool retrieval process for supported assets")

	// Validate GRPC client
	if grpcClient == nil {
		return nil, errors.New("GRPC client cannot be nil")
	}

	// Validate supported tokens
	if len(supportedTokens) == 0 {
		return nil, errors.New("supported tokens list cannot be empty")
	}

	// Create a map for fast token lookup
	supportedTokenMap := make(map[string]bool)
	for _, token := range supportedTokens {
		if strings.TrimSpace(token) == "" {
			return nil, errors.New("supported tokens cannot contain empty strings")
		}
		supportedTokenMap[token] = true
	}

	poolLogger.Info().Int("uniqueSupportedTokens", len(supportedTokenMap)).Msg("Validated supported tokens")

	// Fetch pools from AMM with strict validation and pagination
	ammClient := amm.NewQueryClient(grpcClient)
	
	// Fetch all pools using pagination to handle large datasets
	var allPools []amm.Pool
	var allExtraInfos []amm.PoolExtraInfo
	var nextKey []byte
	pageLimit := uint64(1000) // Reasonable page size for memory efficiency
	
	for {
		// Configure pagination for each request
		paginationReq := &query.PageRequest{
			Key:        nextKey,
			Limit:      pageLimit,
			CountTotal: false, // We don't need total count for this use case
		}
		
		queryPools, err := ammClient.PoolAll(context.Background(), &amm.QueryAllPoolRequest{
			Days: 7,
			Pagination: paginationReq,
		})
		if err != nil {
			poolLogger.Error().Err(err).Msg("Failed to fetch pools from AMM")
			return nil, fmt.Errorf("AMM pool query failed: %w", err)
		}

		if queryPools == nil {
			poolLogger.Error().Msg("Received nil response from AMM")
			return nil, errors.New("nil response from AMM module")
		}

		// Append results from this page
		allPools = append(allPools, queryPools.Pool...)
		allExtraInfos = append(allExtraInfos, queryPools.ExtraInfos...)
		
		// Check if there are more pages
		if queryPools.Pagination == nil || queryPools.Pagination.NextKey == nil || len(queryPools.Pagination.NextKey) == 0 {
			break
		}
		
		nextKey = queryPools.Pagination.NextKey
		poolLogger.Debug().
			Int("fetchedPools", len(queryPools.Pool)).
			Int("totalPoolsSoFar", len(allPools)).
			Msg("Fetched page of pools, continuing pagination")
	}

	if len(allPools) == 0 {
		poolLogger.Error().Msg("No pools returned from AMM")
		return nil, errors.New("no pools available from AMM module")
	}

	poolLogger.Info().Int("poolCount", len(allPools)).Msg("Successfully fetched all pools from AMM")

	// Filter pools to only include those with supported tokens
	supportedPools, supportedExtraInfos := filterSupportedPools(allPools, allExtraInfos, supportedTokenMap)

	if len(supportedPools) == 0 {
		poolLogger.Warn().
			Int("totalPools", len(allPools)).
			Int("supportedTokens", len(supportedTokenMap)).
			Msg("No pools found with supported tokens")
		return nil, errors.New("no pools found with supported tokens")
	}

	poolLogger.Info().
		Int("totalPools", len(allPools)).
		Int("supportedPools", len(supportedPools)).
		Msg("Filtered pools to supported tokens only")

	// Fetch tokens with strict validation - required for supported pools
	tokenMap, err := GetTokens(grpcClient)
	if err != nil {
		poolLogger.Error().Err(err).Msg("Failed to fetch tokens")
		return nil, fmt.Errorf("token fetch failed: %w", err)
	}

	if len(tokenMap) == 0 {
		poolLogger.Error().Msg("No tokens available")
		return nil, errors.New("no tokens available for pool calculations")
	}

	poolLogger.Info().Int("tokenCount", len(tokenMap)).Msg("Successfully fetched tokens")

	// Get token metadata with strict validation
	tokenEntries, err := FetchAllTokens(grpcClient)
	if err != nil {
		poolLogger.Error().Err(err).Msg("Failed to fetch token metadata")
		return nil, fmt.Errorf("token metadata fetch failed: %w", err)
	}

	if len(tokenEntries) == 0 {
		poolLogger.Error().Msg("No token metadata available")
		return nil, errors.New("no token metadata available")
	}

	poolLogger.Info().Int("tokenEntryCount", len(tokenEntries)).Msg("Successfully fetched token metadata")

	// Create map for quick lookup of token entries by denom
	entryMap := make(map[string]assetprofiletypes.Entry)
	for _, entry := range tokenEntries {
		entryMap[entry.Denom] = entry
		// Also add BaseDenom for easier lookup
		if entry.BaseDenom != entry.Denom {
			entryMap[entry.BaseDenom] = entry
		}
	}

	// Fetch volume data with strict validation
	volumeData, err := GetWeeklyVolumeByPool()
	if err != nil {
		poolLogger.Error().Err(err).Msg("Failed to fetch weekly volume data")
		return nil, fmt.Errorf("volume data fetch failed: %w", err)
	}

	poolLogger.Info().Int("volumePoolCount", len(volumeData)).Msg("Successfully fetched weekly volume data")

	// Fetch pool APRs with strict validation
	poolAPRs, err := getPoolAPRs(grpcClient)
	if err != nil {
		poolLogger.Error().Err(err).Msg("Failed to fetch pool APRs")
		return nil, fmt.Errorf("pool APR fetch failed: %w", err)
	}

	poolLogger.Info().Int("poolAPRCount", len(poolAPRs)).Msg("Successfully fetched pool APRs")

	// Validate we have enough extra info for all supported pools
	if len(supportedExtraInfos) != len(supportedPools) {
		poolLogger.Error().
			Int("supportedPoolCount", len(supportedPools)).
			Int("supportedExtraInfoCount", len(supportedExtraInfos)).
			Msg("Mismatch between supported pool count and extra info count")
		return nil, errors.New("incomplete pool data: missing extra info for some supported pools")
	}

	var pools []types.Pool
	processedCount := 0

	for i, pool := range supportedPools {
		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Int("poolIndex", i).
			Msg("Processing pool")

		// Strict validation of AMM pool data
		if err := validateAMMPool(pool); err != nil {
			poolLogger.Error().
				Err(err).
				Uint64("poolID", pool.PoolId).
				Msg("AMM pool validation failed")
			return nil, fmt.Errorf("pool %d validation failed: %w", pool.PoolId, err)
		}

		// Validate extra info exists and is valid
		extraInfo := supportedExtraInfos[i]
		if err := validatePoolExtraInfo(pool.PoolId, extraInfo); err != nil {
			poolLogger.Error().
				Err(err).
				Uint64("poolID", pool.PoolId).
				Msg("Pool extra info validation failed")
			return nil, fmt.Errorf("pool %d extra info validation failed: %w", pool.PoolId, err)
		}

		var newPool types.Pool

		// Log pool data for debugging
		poolJSON, _ := json.Marshal(pool)
		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			RawJSON("poolData", poolJSON).
			Msg("Processing pool data")

		newPool.ID = types.PoolID(pool.PoolId)

		// Get the balances from pool assets
		balanceA := pool.PoolAssets[0].Token.Amount
		balanceB := pool.PoolAssets[1].Token.Amount

		// Get token denoms from pool assets
		denomA := pool.PoolAssets[0].Token.Denom
		denomB := pool.PoolAssets[1].Token.Denom

		// Get tokens from map - both must exist for financial calculations
		tokenA, hasTokenA := tokenMap[denomA]
		tokenB, hasTokenB := tokenMap[denomB]

		if !hasTokenA {
			poolLogger.Error().
				Str("token", denomA).
				Uint64("poolID", pool.PoolId).
				Msg("Token A not found in validated token map")
			return nil, fmt.Errorf("pool %d token A (%s) not found in validated token data", pool.PoolId, denomA)
		}

		if !hasTokenB {
			poolLogger.Error().
				Str("token", denomB).
				Uint64("poolID", pool.PoolId).
				Msg("Token B not found in validated token map")
			return nil, fmt.Errorf("pool %d token B (%s) not found in validated token data", pool.PoolId, denomB)
		}

		newPool.TokenA = tokenA
		newPool.TokenB = tokenB

		// Ensure TokenA is not USDC by swapping if needed
		if newPool.TokenA.Symbol == "USDC" {
			// Swap TokenA and TokenB
			newPool.TokenA, newPool.TokenB = newPool.TokenB, newPool.TokenA
			// Also swap the balances to maintain consistency
			newPool.BalanceA, newPool.BalanceB = balanceB, balanceA

			poolLogger.Debug().
				Uint64("poolID", pool.PoolId).
				Str("newTokenA", newPool.TokenA.Symbol).
				Str("newTokenB", newPool.TokenB.Symbol).
				Msg("Swapped tokenA and tokenB to ensure TokenA is not USDC")
		} else {
			// Assign balances normally
			newPool.BalanceA = balanceA
			newPool.BalanceB = balanceB
		}

		// Calculate actual USD-based weights using token balances and prices
		// Convert raw amounts to human-readable amounts using token precision
		precisionFactorA := sdkmath.NewIntFromUint64(uint64(math.Pow10(newPool.TokenA.Precision)))
		precisionFactorB := sdkmath.NewIntFromUint64(uint64(math.Pow10(newPool.TokenB.Precision)))

		// Validate precision factors
		if precisionFactorA.IsZero() || precisionFactorB.IsZero() {
			poolLogger.Error().
				Uint64("poolID", pool.PoolId).
				Int("precisionA", newPool.TokenA.Precision).
				Int("precisionB", newPool.TokenB.Precision).
				Msg("Invalid precision factors")
			return nil, fmt.Errorf("pool %d has invalid precision factors", pool.PoolId)
		}

		// Convert to decimal by dividing by precision factor
		humanAmountA, err := sdkmath.LegacyNewDecFromInt(newPool.BalanceA).QuoInt(precisionFactorA).Float64()
		if err != nil {
			return nil, fmt.Errorf("pool %d token A amount conversion failed: %w", pool.PoolId, err)
		}
		humanAmountB, err := sdkmath.LegacyNewDecFromInt(newPool.BalanceB).QuoInt(precisionFactorB).Float64()
		if err != nil {
			return nil, fmt.Errorf("pool %d token B amount conversion failed: %w", pool.PoolId, err)
		}

		// Validate converted amounts
		if math.IsNaN(humanAmountA) || math.IsInf(humanAmountA, 0) || humanAmountA <= 0 {
			return nil, fmt.Errorf("pool %d token A has invalid human amount: %f", pool.PoolId, humanAmountA)
		}
		if math.IsNaN(humanAmountB) || math.IsInf(humanAmountB, 0) || humanAmountB <= 0 {
			return nil, fmt.Errorf("pool %d token B has invalid human amount: %f", pool.PoolId, humanAmountB)
		}

		// Calculate USD values
		usdValueA := humanAmountA * newPool.TokenA.PriceUSD
		usdValueB := humanAmountB * newPool.TokenB.PriceUSD
		totalUSDValue := usdValueA + usdValueB

		// Validate USD calculations
		if math.IsNaN(usdValueA) || math.IsInf(usdValueA, 0) || usdValueA < 0 {
			return nil, fmt.Errorf("pool %d token A has invalid USD value: %f", pool.PoolId, usdValueA)
		}
		if math.IsNaN(usdValueB) || math.IsInf(usdValueB, 0) || usdValueB < 0 {
			return nil, fmt.Errorf("pool %d token B has invalid USD value: %f", pool.PoolId, usdValueB)
		}
		if totalUSDValue <= 0 {
			return nil, fmt.Errorf("pool %d has invalid total USD value: %f", pool.PoolId, totalUSDValue)
		}

		// Calculate weight percentages based on actual USD values
		newPool.WeightA = usdValueA / totalUSDValue
		newPool.WeightB = usdValueB / totalUSDValue

		// Validate weights
		if math.IsNaN(newPool.WeightA) || math.IsInf(newPool.WeightA, 0) || newPool.WeightA <= 0 || newPool.WeightA >= 1 {
			return nil, fmt.Errorf("pool %d has invalid weight A: %f", pool.PoolId, newPool.WeightA)
		}
		if math.IsNaN(newPool.WeightB) || math.IsInf(newPool.WeightB, 0) || newPool.WeightB <= 0 || newPool.WeightB >= 1 {
			return nil, fmt.Errorf("pool %d has invalid weight B: %f", pool.PoolId, newPool.WeightB)
		}

		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Str("tokenA", newPool.TokenA.Symbol).
			Str("tokenB", newPool.TokenB.Symbol).
			Float64("humanAmountA", humanAmountA).
			Float64("humanAmountB", humanAmountB).
			Float64("priceA", newPool.TokenA.PriceUSD).
			Float64("priceB", newPool.TokenB.PriceUSD).
			Float64("usdValueA", usdValueA).
			Float64("usdValueB", usdValueB).
			Float64("weightA", newPool.WeightA).
			Float64("weightB", newPool.WeightB).
			Msg("Calculated USD-based pool weights")

		// Get TVL and other pool metrics (already validated above)
		newPool.TvlUSD = extraInfo.Tvl.MustFloat64()
		newPool.PriceImpactAPR = extraInfo.LpSavedApr.MustFloat64()

		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Float64("tvlUSD", newPool.TvlUSD).
			Float64("priceImpactAPR", newPool.PriceImpactAPR).
			Msg("Pool metrics retrieved")

		newPool.SwapFee = pool.PoolParams.SwapFee.MustFloat64()
		newPool.TotalShares = pool.TotalShares.Amount
		newPool.IsSmartShielded = pool.PoolParams.UseOracle

		// Get APRs - must exist for financial calculations
		apr, hasAPR := poolAPRs[pool.PoolId]
		if !hasAPR {
			poolLogger.Error().
				Uint64("poolID", pool.PoolId).
				Msg("APR data not found for pool")
			return nil, fmt.Errorf("pool %d APR data not found", pool.PoolId)
		}

		// Validate APR data
		if err := validatePoolAPR(pool.PoolId, apr); err != nil {
			poolLogger.Error().
				Err(err).
				Uint64("poolID", pool.PoolId).
				Msg("Pool APR validation failed")
			return nil, fmt.Errorf("pool %d APR validation failed: %w", pool.PoolId, err)
		}

		newPool.UsdcFeesAPR = apr.UsdcDexApr.MustFloat64()
		newPool.EdenRewardsAPR = apr.EdenApr.MustFloat64()

		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Float64("usdcFeesAPR", newPool.UsdcFeesAPR).
			Float64("edenRewardsAPR", newPool.EdenRewardsAPR).
			Msg("Pool APRs retrieved")

		// Set age
		// ! To:Do Make this fetch from chain data somehow, not a big deal for now
		newPool.AgeInDays = 30

		// Calculate 7-day volume - handle missing data based on environment
		poolVolume, volumeExists := volumeData[pool.PoolId]
		if !volumeExists {
			// In development environment, handle missing volume data gracefully
			if os.Getenv("ENV") == "dev" {
				poolLogger.Warn().
					Uint64("poolID", pool.PoolId).
					Msg("Volume data not found for pool in dev environment - setting volume to 1 USD")

				newPool.Volume7dUSD = 1.0
			} else {
				// In production/testnet, missing volume data is a critical error
				poolLogger.Error().
					Uint64("poolID", pool.PoolId).
					Msg("Volume data not found for pool")
				return nil, fmt.Errorf("pool %d volume data not found", pool.PoolId)
			}
		} else {
			// Calculate volume from available data
			var totalVolume uint64
			for _, volume := range poolVolume {
				totalVolume += volume
			}
			newPool.Volume7dUSD = float64(totalVolume)
		}

		// Validate volume
		if math.IsNaN(newPool.Volume7dUSD) || math.IsInf(newPool.Volume7dUSD, 0) || newPool.Volume7dUSD < 0 {
			return nil, fmt.Errorf("pool %d has invalid volume: %f", pool.PoolId, newPool.Volume7dUSD)
		}

		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Float64("volume7dUSD", newPool.Volume7dUSD).
			Msg("Pool volume data retrieved")

		// Final comprehensive validation of the complete pool
		if err := validateFinalPool(newPool); err != nil {
			poolLogger.Error().
				Err(err).
				Uint64("poolID", pool.PoolId).
				Msg("Final pool validation failed")
			return nil, fmt.Errorf("final validation failed for pool %d: %w", pool.PoolId, err)
		}

		pools = append(pools, newPool)
		processedCount++

		poolLogger.Debug().
			Uint64("poolID", pool.PoolId).
			Str("tokenA", newPool.TokenA.Symbol).
			Str("tokenB", newPool.TokenB.Symbol).
			Float64("tvl", newPool.TvlUSD).
			Float64("volume", newPool.Volume7dUSD).
			Msg("Successfully processed and validated pool")
	}

	if processedCount == 0 {
		poolLogger.Error().Msg("No pools were successfully processed")
		return nil, errors.New("no valid pools found for financial calculations")
	}

	poolLogger.Info().
		Int("totalPoolsFromAMM", len(allPools)).
		Int("supportedPools", len(supportedPools)).
		Int("processedPools", processedCount).
		Msg("Pool retrieval complete with strict validation - only supported tokens processed")

	return pools, nil
}

// getPoolAPRs fetches pool APRs with strict validation
func getPoolAPRs(grpcClient *grpc.ClientConn) (map[uint64]masterchef.PoolApr, error) {
	if grpcClient == nil {
		return nil, errors.New("GRPC client cannot be nil")
	}

	poolLogger.Debug().Msg("Fetching pool APRs from masterchef module")
	masterchefClient := masterchef.NewQueryClient(grpcClient)

	queryPools, err := masterchefClient.PoolAprs(context.Background(), &masterchef.QueryPoolAprsRequest{})
	if err != nil {
		poolLogger.Error().Err(err).Msg("Failed to fetch pool APRs from masterchef module")
		return nil, fmt.Errorf("masterchef pool APR query failed: %w", err)
	}

	if queryPools == nil {
		poolLogger.Error().Msg("Received nil response from masterchef module")
		return nil, errors.New("nil response from masterchef module")
	}

	if len(queryPools.Data) == 0 {
		poolLogger.Error().Msg("No pool APRs returned from masterchef module")
		return nil, errors.New("no pool APRs available from masterchef module")
	}

	// Create a map of pool APRs keyed by pool ID
	poolAPRMap := make(map[uint64]masterchef.PoolApr)
	validAPRCount := 0

	for _, apr := range queryPools.Data {
		// Basic validation
		if apr.PoolId == 0 {
			poolLogger.Error().Msg("Received APR entry with zero pool ID")
			return nil, errors.New("received APR entry with zero pool ID")
		}

		// Check for duplicates
		if _, exists := poolAPRMap[apr.PoolId]; exists {
			poolLogger.Error().
				Uint64("poolID", apr.PoolId).
				Msg("Duplicate APR entry found")
			return nil, fmt.Errorf("duplicate APR entry for pool %d", apr.PoolId)
		}

		poolAPRMap[apr.PoolId] = apr
		validAPRCount++

		poolLogger.Debug().
			Uint64("poolID", apr.PoolId).
			Str("edenAPR", apr.EdenApr.String()).
			Str("usdcDexAPR", apr.UsdcDexApr.String()).
			Msg("Pool APR data retrieved")
	}

	if validAPRCount == 0 {
		poolLogger.Error().Msg("No valid pool APRs found")
		return nil, errors.New("no valid pool APRs available")
	}

	poolLogger.Info().
		Int("totalAPRs", len(queryPools.Data)).
		Int("validAPRs", validAPRCount).
		Msg("Successfully retrieved and validated pool APRs")

	return poolAPRMap, nil
}

// filterSupportedPools filters pools to only include those with supported tokens
func filterSupportedPools(pools []amm.Pool, extraInfos []amm.PoolExtraInfo, supportedTokens map[string]bool) ([]amm.Pool, []amm.PoolExtraInfo) {
	var supportedPools []amm.Pool
	var supportedExtraInfos []amm.PoolExtraInfo

	for i, pool := range pools {
		// Check if both tokens in the pool are supported
		if len(pool.PoolAssets) != 2 {
			poolLogger.Debug().
				Uint64("poolID", pool.PoolId).
				Int("assetCount", len(pool.PoolAssets)).
				Msg("Skipping pool with unexpected asset count")
			continue
		}

		tokenA := pool.PoolAssets[0].Token.Denom
		tokenB := pool.PoolAssets[1].Token.Denom

		// Both tokens must be supported AND the specific pool ID must be allowed
		poolDenom := fmt.Sprintf("amm/pool/%d", pool.PoolId)
		tokensSupported := supportedTokens[tokenA] && supportedTokens[tokenB]
		poolAllowed := supportedTokens[poolDenom]

		if tokensSupported && poolAllowed {
			supportedPools = append(supportedPools, pool)
			if i < len(extraInfos) {
				supportedExtraInfos = append(supportedExtraInfos, extraInfos[i])
			}

			poolLogger.Debug().
				Uint64("poolID", pool.PoolId).
				Str("tokenA", tokenA).
				Str("tokenB", tokenB).
				Str("poolDenom", poolDenom).
				Msg("Pool included - tokens and pool ID are both supported")
		} else {
			poolLogger.Debug().
				Uint64("poolID", pool.PoolId).
				Str("tokenA", tokenA).
				Str("tokenB", tokenB).
				Str("poolDenom", poolDenom).
				Bool("tokenASupported", supportedTokens[tokenA]).
				Bool("tokenBSupported", supportedTokens[tokenB]).
				Bool("poolAllowed", poolAllowed).
				Msg("Pool skipped - tokens or pool ID not supported")
		}
	}

	return supportedPools, supportedExtraInfos
}

// validateAMMPool performs strict validation on AMM pool data
func validateAMMPool(pool amm.Pool) error {
	// Validate pool ID
	if pool.PoolId == 0 {
		return errors.New("pool ID cannot be zero")
	}

	// Validate pool assets
	if len(pool.PoolAssets) != 2 {
		return fmt.Errorf("pool %d must have exactly 2 assets, found %d", pool.PoolId, len(pool.PoolAssets))
	}

	// Validate each asset
	for i, asset := range pool.PoolAssets {
		if strings.TrimSpace(asset.Token.Denom) == "" {
			return fmt.Errorf("pool %d asset %d has empty denom", pool.PoolId, i)
		}

		if asset.Token.Amount.IsNil() || asset.Token.Amount.IsNegative() {
			return fmt.Errorf("pool %d asset %d has invalid amount: %s", pool.PoolId, i, asset.Token.Amount.String())
		}

		if asset.Token.Amount.IsZero() {
			return fmt.Errorf("pool %d asset %d has zero amount", pool.PoolId, i)
		}
	}

	// Validate assets are different
	if pool.PoolAssets[0].Token.Denom == pool.PoolAssets[1].Token.Denom {
		return fmt.Errorf("pool %d has duplicate token denoms: %s", pool.PoolId, pool.PoolAssets[0].Token.Denom)
	}

	// Validate pool parameters exist (PoolParams is a struct, not a pointer)
	// We can validate its fields instead

	// Validate swap fee
	if pool.PoolParams.SwapFee.IsNil() || pool.PoolParams.SwapFee.IsNegative() {
		return fmt.Errorf("pool %d has invalid swap fee: %s", pool.PoolId, pool.PoolParams.SwapFee.String())
	}

	// Validate total shares
	if pool.TotalShares.Amount.IsNil() || pool.TotalShares.Amount.IsNegative() || pool.TotalShares.Amount.IsZero() {
		return fmt.Errorf("pool %d has invalid total shares: %s", pool.PoolId, pool.TotalShares.Amount.String())
	}

	return nil
}

// validatePoolExtraInfo performs strict validation on pool extra information
func validatePoolExtraInfo(poolID uint64, extraInfo amm.PoolExtraInfo) error {
	// Validate TVL
	if extraInfo.Tvl.IsNil() || extraInfo.Tvl.IsNegative() {
		return fmt.Errorf("pool %d has invalid TVL: %s", poolID, extraInfo.Tvl.String())
	}

	// Validate LP saved APR (price impact APR)
	if extraInfo.LpSavedApr.IsNil() {
		return fmt.Errorf("pool %d has nil LP saved APR", poolID)
	}

	// Convert to float64 to check for NaN/Inf
	tvlFloat, err := extraInfo.Tvl.Float64()
	if err != nil {
		return fmt.Errorf("pool %d TVL conversion failed: %w", poolID, err)
	}
	if math.IsNaN(tvlFloat) || math.IsInf(tvlFloat, 0) {
		return fmt.Errorf("pool %d TVL is not finite: %f", poolID, tvlFloat)
	}

	aprFloat, err := extraInfo.LpSavedApr.Float64()
	if err != nil {
		return fmt.Errorf("pool %d LP saved APR conversion failed: %w", poolID, err)
	}
	if math.IsNaN(aprFloat) || math.IsInf(aprFloat, 0) {
		return fmt.Errorf("pool %d LP saved APR is not finite: %f", poolID, aprFloat)
	}

	return nil
}

// validatePoolAPR performs strict validation on pool APR data
func validatePoolAPR(poolID uint64, apr masterchef.PoolApr) error {
	if apr.PoolId != poolID {
		return fmt.Errorf("APR pool ID mismatch: expected %d, got %d", poolID, apr.PoolId)
	}

	// Validate Eden APR
	if apr.EdenApr.IsNil() {
		return fmt.Errorf("pool %d has nil Eden APR", poolID)
	}

	// Validate USDC DEX APR
	if apr.UsdcDexApr.IsNil() {
		return fmt.Errorf("pool %d has nil USDC DEX APR", poolID)
	}

	// Convert to float64 to check for NaN/Inf
	edenFloat, err := apr.EdenApr.Float64()
	if err != nil {
		return fmt.Errorf("pool %d Eden APR conversion failed: %w", poolID, err)
	}
	if math.IsNaN(edenFloat) || math.IsInf(edenFloat, 0) {
		return fmt.Errorf("pool %d Eden APR is not finite: %f", poolID, edenFloat)
	}

	usdcFloat, err := apr.UsdcDexApr.Float64()
	if err != nil {
		return fmt.Errorf("pool %d USDC DEX APR conversion failed: %w", poolID, err)
	}
	if math.IsNaN(usdcFloat) || math.IsInf(usdcFloat, 0) {
		return fmt.Errorf("pool %d USDC DEX APR is not finite: %f", poolID, usdcFloat)
	}

	return nil
}

// validateTokenForPool ensures a token has all required data for pool calculations
func validateTokenForPool(token types.Token, poolID uint64, tokenLabel string) error {
	// Basic validation
	if strings.TrimSpace(token.Symbol) == "" {
		return fmt.Errorf("pool %d %s has empty symbol", poolID, tokenLabel)
	}
	if strings.TrimSpace(token.Denom) == "" {
		return fmt.Errorf("pool %d %s has empty denom", poolID, tokenLabel)
	}
	if strings.TrimSpace(token.IBCDenom) == "" {
		return fmt.Errorf("pool %d %s has empty IBC denom", poolID, tokenLabel)
	}

	// Precision validation
	if token.Precision <= 0 {
		return fmt.Errorf("pool %d %s has invalid precision: %d", poolID, tokenLabel, token.Precision)
	}

	// Price validation - critical for financial calculations
	if token.PriceUSD <= 0 {
		return fmt.Errorf("pool %d %s has invalid USD price: %f", poolID, tokenLabel, token.PriceUSD)
	}
	if math.IsNaN(token.PriceUSD) || math.IsInf(token.PriceUSD, 0) {
		return fmt.Errorf("pool %d %s USD price is not finite: %f", poolID, tokenLabel, token.PriceUSD)
	}

	// Volatility validation - required for risk calculations
	if token.Volatility < 0 {
		return fmt.Errorf("pool %d %s has negative volatility: %f", poolID, tokenLabel, token.Volatility)
	}
	if math.IsNaN(token.Volatility) || math.IsInf(token.Volatility, 0) {
		return fmt.Errorf("pool %d %s volatility is not finite: %f", poolID, tokenLabel, token.Volatility)
	}

	return nil
}

// validateFinalPool performs comprehensive validation on the final pool structure
func validateFinalPool(pool types.Pool) error {
	// Validate pool ID
	if pool.ID == 0 {
		return errors.New("final pool has zero ID")
	}

	// Validate tokens
	if err := validateTokenForPool(pool.TokenA, uint64(pool.ID), "TokenA"); err != nil {
		return fmt.Errorf("TokenA validation failed: %w", err)
	}
	if err := validateTokenForPool(pool.TokenB, uint64(pool.ID), "TokenB"); err != nil {
		return fmt.Errorf("TokenB validation failed: %w", err)
	}

	// Ensure tokens are different
	if pool.TokenA.Denom == pool.TokenB.Denom {
		return fmt.Errorf("pool %d has duplicate token denoms: %s", pool.ID, pool.TokenA.Denom)
	}

	// Validate balances
	if pool.BalanceA.IsNil() || pool.BalanceA.IsNegative() || pool.BalanceA.IsZero() {
		return fmt.Errorf("pool %d has invalid balance A: %s", pool.ID, pool.BalanceA.String())
	}
	if pool.BalanceB.IsNil() || pool.BalanceB.IsNegative() || pool.BalanceB.IsZero() {
		return fmt.Errorf("pool %d has invalid balance B: %s", pool.ID, pool.BalanceB.String())
	}

	// Validate weights
	if pool.WeightA <= 0 || pool.WeightA >= 1 {
		return fmt.Errorf("pool %d has invalid weight A: %f", pool.ID, pool.WeightA)
	}
	if pool.WeightB <= 0 || pool.WeightB >= 1 {
		return fmt.Errorf("pool %d has invalid weight B: %f", pool.ID, pool.WeightB)
	}
	if math.IsNaN(pool.WeightA) || math.IsInf(pool.WeightA, 0) {
		return fmt.Errorf("pool %d weight A is not finite: %f", pool.ID, pool.WeightA)
	}
	if math.IsNaN(pool.WeightB) || math.IsInf(pool.WeightB, 0) {
		return fmt.Errorf("pool %d weight B is not finite: %f", pool.ID, pool.WeightB)
	}

	// Weights should sum to approximately 1.0
	weightSum := pool.WeightA + pool.WeightB
	if math.Abs(weightSum-1.0) > 0.01 {
		return fmt.Errorf("pool %d weights don't sum to 1.0: %f", pool.ID, weightSum)
	}

	// Validate financial metrics
	if pool.TvlUSD < 0 {
		return fmt.Errorf("pool %d has negative TVL: %f", pool.ID, pool.TvlUSD)
	}
	if math.IsNaN(pool.TvlUSD) || math.IsInf(pool.TvlUSD, 0) {
		return fmt.Errorf("pool %d TVL is not finite: %f", pool.ID, pool.TvlUSD)
	}

	if pool.Volume7dUSD < 0 {
		return fmt.Errorf("pool %d has negative volume: %f", pool.ID, pool.Volume7dUSD)
	}
	if math.IsNaN(pool.Volume7dUSD) || math.IsInf(pool.Volume7dUSD, 0) {
		return fmt.Errorf("pool %d volume is not finite: %f", pool.ID, pool.Volume7dUSD)
	}

	// Validate APR values
	aprValues := []struct {
		value float64
		name  string
	}{
		{pool.EdenRewardsAPR, "Eden rewards APR"},
		{pool.UsdcFeesAPR, "USDC fees APR"},
		{pool.PriceImpactAPR, "price impact APR"},
	}

	for _, apr := range aprValues {
		if math.IsNaN(apr.value) || math.IsInf(apr.value, 0) {
			return fmt.Errorf("pool %d %s is not finite: %f", pool.ID, apr.name, apr.value)
		}
	}

	// Validate swap fee
	if pool.SwapFee < 0 || pool.SwapFee > 1 {
		return fmt.Errorf("pool %d has invalid swap fee: %f", pool.ID, pool.SwapFee)
	}
	if math.IsNaN(pool.SwapFee) || math.IsInf(pool.SwapFee, 0) {
		return fmt.Errorf("pool %d swap fee is not finite: %f", pool.ID, pool.SwapFee)
	}

	// Validate total shares
	if pool.TotalShares.IsNil() || pool.TotalShares.IsNegative() || pool.TotalShares.IsZero() {
		return fmt.Errorf("pool %d has invalid total shares: %s", pool.ID, pool.TotalShares.String())
	}

	// Validate age
	if pool.AgeInDays < 0 {
		return fmt.Errorf("pool %d has negative age: %d", pool.ID, pool.AgeInDays)
	}

	return nil
}

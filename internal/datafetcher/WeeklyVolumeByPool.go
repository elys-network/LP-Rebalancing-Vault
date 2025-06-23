package datafetcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elys-network/avm/internal/config"
	"github.com/elys-network/avm/internal/logger"
)

var volumeLogger = logger.GetForComponent("volume_retriever")
var ErrInvalidVolumeData = errors.New("invalid volume data")
var ErrInsufficientVolumeData = errors.New("insufficient volume data for financial calculations")
var ErrAPIResponseInvalid = errors.New("API response validation failed")

const (
	VOLUME_API_ROUTE = "/stats/weekly-volume-by-pool"
	VOLUME_TIMEOUT = 30 * time.Second
)

// WeeklyVolumeData represents the structure of each volume data entry
type WeeklyVolumeData struct {
	Date         string `json:"date"`
	PoolID       string `json:"pool_id"`
	TokenADenom  string `json:"token_a_denom"`
	TokenAVolume uint64 `json:"token_a_volume"`
	TokenBDenom  string `json:"token_b_denom"`
	TokenBVolume uint64 `json:"token_b_volume"`
}

// PoolVolumeMap represents the volume data for each pool
type PoolVolumeMap map[uint64]map[string]uint64

// GetWeeklyVolumeByPool fetches the weekly volume data for all pools with strict validation
func GetWeeklyVolumeByPool() (PoolVolumeMap, error) {
	volumeLogger.Info().Msg("Starting strict weekly volume data retrieval")

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: VOLUME_TIMEOUT,
	}

	// Fetch data from the Elys Supply Stats API
	volumeLogger.Debug().
		Str("url", config.SupplyAPI + VOLUME_API_ROUTE).
		Dur("timeout", VOLUME_TIMEOUT).
		Msg("Making API request for volume data")

	resp, err := client.Get(config.SupplyAPI + VOLUME_API_ROUTE)
	if err != nil {
		volumeLogger.Error().
			Err(err).
			Str("url", config.SupplyAPI + VOLUME_API_ROUTE).
			Msg("HTTP request failed for volume data")
		return nil, fmt.Errorf("volume data API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Strict validation of API response
	if err := validateAPIResponse(resp); err != nil {
		volumeLogger.Error().
			Err(err).
			Int("statusCode", resp.StatusCode).
			Msg("API response validation failed")
		return nil, fmt.Errorf("API response validation failed: %w", err)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		volumeLogger.Error().
			Err(err).
			Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read volume data response: %w", err)
	}

	if len(body) == 0 {
		volumeLogger.Error().Msg("Empty response body from volume API")
		return nil, errors.New("empty response body from volume API")
	}

	// Parse the JSON data
	var volumeData []WeeklyVolumeData
	if err := json.Unmarshal(body, &volumeData); err != nil {
		volumeLogger.Error().
			Err(err).
			Int("bodyLength", len(body)).
			Msg("Failed to parse JSON volume data")
		return nil, fmt.Errorf("failed to parse volume data JSON: %w", err)
	}

	// Filter out invalid entries and validate remaining data
	validVolumeData := make([]WeeklyVolumeData, 0, len(volumeData))
	skippedCount := 0

	for i, data := range volumeData {
		if err := validateVolumeDataEntry(data); err != nil {
			volumeLogger.Warn().
				Err(err).
				Int("entryIndex", i).
				Str("poolID", data.PoolID).
				Str("date", data.Date).
				Msg("Skipping invalid volume data entry")
			skippedCount++
			continue
		}
		validVolumeData = append(validVolumeData, data)
	}

	if len(validVolumeData) == 0 {
		volumeLogger.Error().
			Int("totalEntries", len(volumeData)).
			Int("skippedEntries", skippedCount).
			Msg("No valid volume data entries found")
		return nil, errors.New("no valid volume data entries found")
	}

	volumeLogger.Info().
		Int("totalEntries", len(volumeData)).
		Int("validEntries", len(validVolumeData)).
		Int("skippedEntries", skippedCount).
		Msg("Filtered volume data entries")

	// Use the filtered data for processing
	volumeData = validVolumeData

	// Group data by pool ID and date with strict validation
	poolDateMap := make(map[uint64]map[string]WeeklyVolumeData)
	processedEntries := 0

	for i, data := range volumeData {
		// Convert pool ID from string to uint64 (already validated above)
		poolID, err := strconv.ParseUint(data.PoolID, 10, 64)
		if err != nil {
			// This should not happen due to prior validation, but be safe
			volumeLogger.Error().
				Err(err).
				Str("poolID", data.PoolID).
				Int("entryIndex", i).
				Msg("Pool ID conversion failed during processing")
			return nil, fmt.Errorf("pool ID conversion failed for entry %d: %w", i, err)
		}

		if _, exists := poolDateMap[poolID]; !exists {
			poolDateMap[poolID] = make(map[string]WeeklyVolumeData)
		}

		// Check for duplicate entries
		if _, exists := poolDateMap[poolID][data.Date]; exists {
			volumeLogger.Error().
				Uint64("poolID", poolID).
				Str("date", data.Date).
				Msg("Duplicate volume data entry found")
			return nil, fmt.Errorf("duplicate volume data for pool %d on date %s", poolID, data.Date)
		}

		poolDateMap[poolID][data.Date] = data
		processedEntries++
	}

	if processedEntries == 0 {
		volumeLogger.Error().Msg("No volume data entries were successfully processed")
		return nil, errors.New("no volume data entries could be processed")
	}

	// Initialize the result map
	result := make(PoolVolumeMap)

	// For each pool, find the most recent data with strict validation
	for poolID, dateMap := range poolDateMap {
		if len(dateMap) == 0 {
			volumeLogger.Error().
				Uint64("poolID", poolID).
				Msg("Pool has no date entries")
			return nil, fmt.Errorf("pool %d has no volume data entries", poolID)
		}

		// Get all dates for this pool
		dates := make([]string, 0, len(dateMap))
		for date := range dateMap {
			dates = append(dates, date)
		}

		// Sort dates in descending order (most recent first)
		sort.Slice(dates, func(i, j int) bool {
			t1, err1 := time.Parse(time.RFC3339, dates[i])
			t2, err2 := time.Parse(time.RFC3339, dates[j])

			// Both should parse successfully due to prior validation
			if err1 != nil || err2 != nil {
				volumeLogger.Error().
					Str("date1", dates[i]).
					Str("date2", dates[j]).
					Err(err1).
					Err(err2).
					Msg("Date parsing failed during sorting")
				return false
			}

			return t1.After(t2)
		})

		// Get the most recent data
		mostRecentData := dateMap[dates[0]]

		// Create volume map for this pool
		volumeMap := make(map[string]uint64)
		volumeMap[mostRecentData.TokenADenom] = mostRecentData.TokenAVolume
		volumeMap[mostRecentData.TokenBDenom] = mostRecentData.TokenBVolume

		// Add to result
		result[poolID] = volumeMap

		volumeLogger.Debug().
			Uint64("poolID", poolID).
			Str("mostRecentDate", dates[0]).
			Str("tokenADenom", mostRecentData.TokenADenom).
			Uint64("tokenAVolume", mostRecentData.TokenAVolume).
			Str("tokenBDenom", mostRecentData.TokenBDenom).
			Uint64("tokenBVolume", mostRecentData.TokenBVolume).
			Msg("Processed pool volume data")
	}

	// Final validation of result
	if err := validateFinalVolumeMap(result); err != nil {
		volumeLogger.Error().
			Err(err).
			Int("resultPools", len(result)).
			Msg("Final volume map validation failed")
		return nil, fmt.Errorf("final volume data validation failed: %w", err)
	}

	volumeLogger.Info().
		Int("totalEntries", len(volumeData)).
		Int("processedEntries", processedEntries).
		Int("uniquePools", len(poolDateMap)).
		Int("finalPools", len(result)).
		Msg("Successfully retrieved and validated weekly volume data")

	return result, nil
}

// validateVolumeDataEntry performs strict validation on individual volume data entries
func validateVolumeDataEntry(data WeeklyVolumeData) error {
	// Validate date format and content
	if strings.TrimSpace(data.Date) == "" {
		return errors.New("date cannot be empty")
	}

	// Parse and validate date
	parsedDate, err := time.Parse(time.RFC3339, data.Date)
	if err != nil {
		return fmt.Errorf("invalid date format '%s': %w", data.Date, err)
	}

	// Date cannot be in the future (with small tolerance)
	if parsedDate.After(time.Now().Add(24 * time.Hour)) {
		return fmt.Errorf("date cannot be in the future: %s", data.Date)
	}

	// Date cannot be too old (more than 1 year)
	if parsedDate.Before(time.Now().Add(-365 * 24 * time.Hour)) {
		return fmt.Errorf("date is too old for reliable volume data: %s", data.Date)
	}

	// Validate pool ID
	if strings.TrimSpace(data.PoolID) == "" {
		return errors.New("pool ID cannot be empty")
	}

	// Pool ID must be convertible to uint64
	poolIDNum, err := strconv.ParseUint(data.PoolID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid pool ID format '%s': %w", data.PoolID, err)
	}

	// Pool ID must be positive
	if poolIDNum == 0 {
		return fmt.Errorf("pool ID cannot be zero: %s", data.PoolID)
	}

	// Validate token denoms
	if strings.TrimSpace(data.TokenADenom) == "" {
		return fmt.Errorf("token A denom cannot be empty for pool %s", data.PoolID)
	}
	if strings.TrimSpace(data.TokenBDenom) == "" {
		return fmt.Errorf("token B denom cannot be empty for pool %s", data.PoolID)
	}

	// Token denoms must be different
	if data.TokenADenom == data.TokenBDenom {
		return fmt.Errorf("token A and B denoms cannot be the same for pool %s: %s", data.PoolID, data.TokenADenom)
	}

	// Validate volume values are finite (uint64 guarantees non-negative)
	if data.TokenAVolume == 0 && data.TokenBVolume == 0 {
		return fmt.Errorf("both token volumes cannot be zero for pool %s", data.PoolID)
	}

	// Check for reasonable volume values (not impossibly large)
	maxReasonableVolume := uint64(math.MaxUint64 / 1000) // Leave some headroom
	if data.TokenAVolume > maxReasonableVolume {
		return fmt.Errorf("token A volume unreasonably large for pool %s: %d", data.PoolID, data.TokenAVolume)
	}
	if data.TokenBVolume > maxReasonableVolume {
		return fmt.Errorf("token B volume unreasonably large for pool %s: %d", data.PoolID, data.TokenBVolume)
	}

	return nil
}

// validateAPIResponse performs strict validation on the API response
func validateAPIResponse(resp *http.Response) error {
	if resp == nil {
		return errors.New("HTTP response is nil")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned non-200 status: %d", resp.StatusCode)
	}

	if resp.Body == nil {
		return errors.New("response body is nil")
	}

	return nil
}

// validateVolumeDataArray performs validation on the entire volume data array
func validateVolumeDataArray(volumeData []WeeklyVolumeData) error {
	if len(volumeData) == 0 {
		return errors.New("no volume data received from API")
	}

	// Track pools to ensure we have reasonable coverage
	poolCount := make(map[string]bool)

	for i, data := range volumeData {
		if err := validateVolumeDataEntry(data); err != nil {
			return fmt.Errorf("invalid volume data entry at index %d: %w", i, err)
		}
		poolCount[data.PoolID] = true
	}

	// Must have at least one pool
	if len(poolCount) == 0 {
		return errors.New("no valid pools found in volume data")
	}

	volumeLogger.Debug().
		Int("totalEntries", len(volumeData)).
		Int("uniquePools", len(poolCount)).
		Msg("Volume data array validation passed")

	return nil
}

// validateFinalVolumeMap ensures the final result meets requirements for financial calculations
func validateFinalVolumeMap(result PoolVolumeMap) error {
	if len(result) == 0 {
		return errors.New("no pools with valid volume data found")
	}

	for poolID, volumeMap := range result {
		if poolID == 0 {
			return errors.New("pool ID cannot be zero in final result")
		}

		if len(volumeMap) == 0 {
			return fmt.Errorf("pool %d has no volume data", poolID)
		}

		// Each pool should have exactly 2 tokens
		if len(volumeMap) != 2 {
			return fmt.Errorf("pool %d has invalid number of tokens: %d (expected 2)", poolID, len(volumeMap))
		}

		// Validate each token's volume
		totalVolume := uint64(0)
		for denom, volume := range volumeMap {
			if strings.TrimSpace(denom) == "" {
				return fmt.Errorf("empty token denom found in pool %d", poolID)
			}

			// At least one token should have non-zero volume
			totalVolume += volume
		}

		if totalVolume == 0 {
			return fmt.Errorf("pool %d has zero total volume", poolID)
		}
	}

	return nil
}

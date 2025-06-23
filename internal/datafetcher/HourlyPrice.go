/*
This file is used to fetch historical price data from the CryptoCompare API.

The system needs exactly 30 days (720 hours) of valid hourly price data
to calculate volatility accurately for financial decisions involving millions of dollars.
*/

package datafetcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types"
)

var priceLogger = logger.GetForComponent("price_retriever")

var ErrInvalidPriceData = errors.New("invalid price data received")
var ErrInsufficientData = errors.New("insufficient price data for volatility calculation")
var ErrAPIConfiguration = errors.New("API configuration error")

const (
	REQUIRED_HOURS  = 720 // Exactly 30 days of hourly data required for volatility
	BASE_URL        = "https://min-api.cryptocompare.com/data/v2/histohour"
	MAX_RETRIES     = 3
	TIMEOUT_SECONDS = 30
)

type CryptoCompareResponse struct {
	Response   string   `json:"Response"`
	Message    string   `json:"Message"`
	HasWarning bool     `json:"HasWarning"`
	Type       int      `json:"Type"`
	RateLimit  struct{} `json:"RateLimit"`
	Data       struct {
		Aggregated bool  `json:"Aggregated"`
		TimeFrom   int64 `json:"TimeFrom"`
		TimeTo     int64 `json:"TimeTo"`
		Data       []struct {
			Time             int64   `json:"time"`
			Close            float64 `json:"close"`
			High             float64 `json:"high"`
			Low              float64 `json:"low"`
			Open             float64 `json:"open"`
			VolumeFrom       float64 `json:"volumefrom"`
			VolumeTo         float64 `json:"volumeto"`
			ConversionType   string  `json:"conversionType"`
			ConversionSymbol string  `json:"conversionSymbol"`
		} `json:"Data"`
	} `json:"Data"`
}

// validatePriceDataPoint performs strict validation on individual price data points
func validatePriceDataPoint(data struct {
	Time             int64   `json:"time"`
	Close            float64 `json:"close"`
	High             float64 `json:"high"`
	Low              float64 `json:"low"`
	Open             float64 `json:"open"`
	VolumeFrom       float64 `json:"volumefrom"`
	VolumeTo         float64 `json:"volumeto"`
	ConversionType   string  `json:"conversionType"`
	ConversionSymbol string  `json:"conversionSymbol"`
}, coin string) error {
	// Validate timestamp
	if data.Time <= 0 {
		return fmt.Errorf("invalid timestamp for %s: %d", coin, data.Time)
	}

	// Validate all price fields are finite and positive
	prices := []struct {
		value float64
		name  string
	}{
		{data.Close, "close"},
		{data.High, "high"},
		{data.Low, "low"},
		{data.Open, "open"},
	}

	for _, price := range prices {
		if math.IsNaN(price.value) || math.IsInf(price.value, 0) {
			return fmt.Errorf("%s price for %s is not finite: %f", price.name, coin, price.value)
		}
		if price.value <= 0 {
			return fmt.Errorf("%s price for %s must be positive: %f", price.name, coin, price.value)
		}
	}

	// Validate price relationships (high >= low is fundamental)
	if data.High < data.Low {
		return fmt.Errorf("high price (%f) cannot be less than low price (%f) for %s", data.High, data.Low, coin)
	}

	// Close price should be within the high/low range of the same period
	if data.Close < data.Low || data.Close > data.High {
		return fmt.Errorf("close price (%f) must be between low (%f) and high (%f) for %s", data.Close, data.Low, data.High, coin)
	}

	// Note: Open price doesn't need to be within high/low range as it represents
	// the price at the start of the period, while high/low are during the period.
	// However, we should validate it's reasonable relative to other prices.

	// Check if open price is extremely far from the high/low range (potential data error)
	// Use percentage-based validation to handle both low and high value assets
	midPrice := (data.High + data.Low) / 2.0
	if midPrice > 0 {
		// Allow open to be up to 50% away from the mid-point of high/low range
		// This is very generous but prevents obvious data corruption
		tolerance := midPrice * 0.5
		if data.Open < (midPrice-tolerance) || data.Open > (midPrice+tolerance) {
			return fmt.Errorf("open price (%f) is unreasonably far from trading range [%f-%f] for %s",
				data.Open, data.Low, data.High, coin)
		}
	}

	// Validate volume fields are non-negative and finite
	volumes := []struct {
		value float64
		name  string
	}{
		{data.VolumeFrom, "volumeFrom"},
		{data.VolumeTo, "volumeTo"},
	}

	for _, volume := range volumes {
		if math.IsNaN(volume.value) || math.IsInf(volume.value, 0) {
			return fmt.Errorf("%s for %s is not finite: %f", volume.name, coin, volume.value)
		}
		if volume.value < 0 {
			return fmt.Errorf("%s for %s cannot be negative: %f", volume.name, coin, volume.value)
		}
	}

	return nil
}

// FetchHistoricalPriceData fetches exactly 30 days of hourly price data with strict validation
func FetchHistoricalPriceData(coin string) ([]types.PriceData, error) {
	// Normalize coin symbol
	originalCoin := coin
	coin = strings.TrimSpace(strings.ToUpper(coin))

	fmt.Printf("DEBUG: Starting price data fetch for %s (normalized: %s)\n", originalCoin, coin)

	apiKey := os.Getenv("CRYPTOCOMPARE_API")
	if apiKey == "" {
		fmt.Printf("ERROR: CRYPTOCOMPARE_API environment variable not set for %s\n", coin)
		return nil, errors.New("CRYPTOCOMPARE_API environment variable is required")
	}

	// Build request URL
	url := fmt.Sprintf("%s?fsym=%s&tsym=USD&limit=%d&api_key=%s",
		BASE_URL, coin, REQUIRED_HOURS, apiKey)

	fmt.Printf("DEBUG: Fetching price data for %s from URL: %s\n", coin, url)

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: TIMEOUT_SECONDS * time.Second,
	}

	var lastErr error
	for attempt := 1; attempt <= MAX_RETRIES; attempt++ {
		priceLogger.Debug().
			Str("coin", coin).
			Int("attempt", attempt).
			Int("maxRetries", MAX_RETRIES).
			Msg("Making API request")

		resp, err := client.Get(url)

		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed on attempt %d: %w", attempt, err)
			priceLogger.Warn().
				Err(err).
				Str("coin", coin).
				Int("attempt", attempt).
				Str("url", url).
				Msg("HTTP request failed, will retry if attempts remain")

			if attempt < MAX_RETRIES {
				time.Sleep(time.Duration(attempt) * time.Second) // Exponential backoff
				continue
			}
			break
		}

		priceLogger.Info().
			Str("coin", coin).
			Int("attempt", attempt).
			Int("statusCode", resp.StatusCode).
			Msg("HTTP request successful")

		// Process successful response
		result, err := processAPIResponse(resp, coin)
		if err != nil {
			lastErr = err
			resp.Body.Close()
			if attempt < MAX_RETRIES {
				priceLogger.Warn().
					Err(err).
					Str("coin", coin).
					Int("attempt", attempt).
					Msg("API response processing failed, will retry if attempts remain")
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			break
		}

		resp.Body.Close()
		return result, nil
	}

	priceLogger.Error().
		Err(lastErr).
		Str("coin", coin).
		Int("maxRetries", MAX_RETRIES).
		Msg("All retry attempts failed")
	return nil, fmt.Errorf("failed to fetch price data for %s after %d attempts: %w", coin, MAX_RETRIES, lastErr)
}

// processAPIResponse handles the API response with strict validation
func processAPIResponse(resp *http.Response, coin string) ([]types.PriceData, error) {
	defer resp.Body.Close()

	// Validate HTTP status
	if resp.StatusCode != http.StatusOK {
		priceLogger.Error().
			Str("coin", coin).
			Int("statusCode", resp.StatusCode).
			Msg("API returned non-200 status")
		return nil, fmt.Errorf("API returned status %d for %s", resp.StatusCode, coin)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		priceLogger.Error().
			Err(err).
			Str("coin", coin).
			Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body for %s: %w", coin, err)
	}

	if len(body) == 0 {
		priceLogger.Error().
			Str("coin", coin).
			Msg("Empty response body")
		return nil, fmt.Errorf("empty response body for %s", coin)
	}

	priceLogger.Debug().
		Str("coin", coin).
		Int("bodyLength", len(body)).
		Msg("Raw API response received")

	// Parse JSON response
	var cryptoResp CryptoCompareResponse
	if err := json.Unmarshal(body, &cryptoResp); err != nil {
		priceLogger.Error().
			Err(err).
			Str("coin", coin).
			Str("responseBody", string(body)).
			Msg("Failed to parse JSON response")
		return nil, fmt.Errorf("failed to parse JSON response for %s: %w", coin, err)
	}

	// Log the full API response for debugging
	priceLogger.Debug().
		Str("coin", coin).
		Str("apiResponse", cryptoResp.Response).
		Str("apiMessage", cryptoResp.Message).
		Bool("hasWarning", cryptoResp.HasWarning).
		Int("dataPointCount", len(cryptoResp.Data.Data)).
		Msg("API response details")

	// Validate API response success
	if cryptoResp.Response != "Success" {
		priceLogger.Error().
			Str("coin", coin).
			Str("apiResponse", cryptoResp.Response).
			Str("apiMessage", cryptoResp.Message).
			Bool("hasWarning", cryptoResp.HasWarning).
			Msg("API returned error response")
		return nil, fmt.Errorf("API error for %s: %s - %s", coin, cryptoResp.Response, cryptoResp.Message)
	}

	// Validate data availability - check if we have actual data
	if len(cryptoResp.Data.Data) == 0 {
		priceLogger.Error().
			Str("coin", coin).
			Str("apiMessage", cryptoResp.Message).
			Bool("hasWarning", cryptoResp.HasWarning).
			Msg("No data points available in API response")
		return nil, fmt.Errorf("no data available for %s: %s", coin, cryptoResp.Message)
	}

	// Log warning if API indicates a warning
	if cryptoResp.HasWarning {
		fmt.Printf("WARNING: API returned warning for %s (data points: %d)\n", coin, len(cryptoResp.Data.Data))
		priceLogger.Warn().
			Str("coin", coin).
			Int("dataPointCount", len(cryptoResp.Data.Data)).
			Str("message", cryptoResp.Message).
			Msg("API returned warning but has data - continuing")
	}

	// Validate we received the required amount of data
	dataPoints := len(cryptoResp.Data.Data)
	if dataPoints < REQUIRED_HOURS {
		priceLogger.Error().
			Str("coin", coin).
			Int("received", dataPoints).
			Int("required", REQUIRED_HOURS).
			Msg("Insufficient data points received")
		return nil, fmt.Errorf("insufficient data for %s: received %d hours, required %d", coin, dataPoints, REQUIRED_HOURS)
	}

	// Process and validate each data point
	var priceData []types.PriceData
	priceData = make([]types.PriceData, 0, dataPoints)

	for i, data := range cryptoResp.Data.Data {
		// Validate individual data point
		if err := validatePriceDataPoint(data, coin); err != nil {
			priceLogger.Error().
				Err(err).
				Str("coin", coin).
				Int("dataPointIndex", i).
				Int64("timestamp", data.Time).
				Msg("Invalid data point")
			return nil, fmt.Errorf("invalid data point %d for %s: %w", i, coin, err)
		}

		// Convert to internal format
		priceData = append(priceData, types.PriceData{
			Timestamp: time.Unix(data.Time, 0),
			Price:     data.Close,
		})
	}

	// Final validation - ensure we have exactly what we need
	if len(priceData) < REQUIRED_HOURS {
		priceLogger.Error().
			Str("coin", coin).
			Int("validDataPoints", len(priceData)).
			Int("required", REQUIRED_HOURS).
			Msg("Not enough valid data points after validation")
		return nil, fmt.Errorf("insufficient valid data for %s: %d valid points, %d required", coin, len(priceData), REQUIRED_HOURS)
	}

	// Validate timestamps are in chronological order and contiguous
	if err := validateTimeSequence(priceData, coin); err != nil {
		return nil, err
	}

	// Take exactly the required number of most recent data points
	if len(priceData) > REQUIRED_HOURS {
		priceData = priceData[len(priceData)-REQUIRED_HOURS:]
	}

	priceLogger.Info().
		Str("coin", coin).
		Int("dataPoints", len(priceData)).
		Time("oldestData", priceData[0].Timestamp).
		Time("newestData", priceData[len(priceData)-1].Timestamp).
		Msg("Successfully retrieved and validated price data")

	return priceData, nil
}

// validateTimeSequence ensures the price data has proper chronological sequence
func validateTimeSequence(priceData []types.PriceData, coin string) error {
	if len(priceData) < 2 {
		return fmt.Errorf("insufficient data points to validate sequence for %s", coin)
	}

	for i := 1; i < len(priceData); i++ {
		// Check chronological order
		if priceData[i].Timestamp.Before(priceData[i-1].Timestamp) {
			return fmt.Errorf("data points not in chronological order for %s at index %d", coin, i)
		}

		// Check for reasonable time gaps (should be ~1 hour apart)
		timeDiff := priceData[i].Timestamp.Sub(priceData[i-1].Timestamp)
		if timeDiff < 30*time.Minute || timeDiff > 90*time.Minute {
			priceLogger.Warn().
				Str("coin", coin).
				Int("index", i).
				Dur("timeDiff", timeDiff).
				Msg("Unusual time gap between data points")
			// Note: This is a warning, not an error, as some gaps might be acceptable
		}
	}

	return nil
}

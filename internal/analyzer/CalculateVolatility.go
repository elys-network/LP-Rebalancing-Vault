package analyzer

import (
	"errors"
	"math"
	"sort"

	"github.com/elys-network/avm/internal/types"
)

// ErrInsufficientData indicates that not enough data points were provided
// to calculate volatility (need at least 2 points for 1 return).
var ErrInsufficientData = errors.New("insufficient data points to calculate volatility")

// CalculateVolatility calculates the annualized historical volatility from a series of price data.
// It assumes the price data is sorted chronologically. If not, it sorts it first.
// It uses logarithmic returns and standard deviation.
// The annualizationFactor should match the frequency of the data (e.g., 8760 for hourly, 365 for daily).
func CalculateVolatility(prices []types.PriceData, annualizationFactor float64) (float64, error) {
	n := len(prices)

	// --- Input Validation ---
	if n < 2 {
		return 0, ErrInsufficientData // Need at least two points to calculate one return
	}

	sort.Slice(prices, func(i, j int) bool {
		return prices[i].Timestamp.Before(prices[j].Timestamp)
	})

	// --- Calculate Logarithmic Returns ---
	logReturns := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		currentPrice := prices[i].Price
		previousPrice := prices[i-1].Price

		// Check for invalid prices that would break math.Log
		if previousPrice <= 0 || currentPrice <= 0 {
			// Optionally log a warning here: log.Printf("Warning: Skipping return calculation due to non-positive price at index %d or %d", i-1, i)
			continue // Skip this data point pair
		}

		logReturn := math.Log(currentPrice / previousPrice)
		logReturns = append(logReturns, logReturn)
	}

	// Check if we could calculate any returns
	numReturns := len(logReturns)
	if numReturns == 0 {
		return 0, ErrInsufficientData // Could happen if all previous prices were <= 0
	}

	// --- Calculate Standard Deviation of Log Returns ---
	// 1. Calculate the mean (average)
	var sum float64
	for _, r := range logReturns {
		sum += r
	}
	mean := sum / float64(numReturns)

	// 2. Calculate sum of squared differences from the mean
	var sumSqDiff float64
	for _, r := range logReturns {
		sumSqDiff += math.Pow(r-mean, 2)
	}

	// 3. Calculate variance (using population standard deviation N, not N-1)
	variance := sumSqDiff / float64(numReturns)

	// 4. Standard deviation is the square root of variance
	stdDev := math.Sqrt(variance)

	// --- Annualize the Standard Deviation ---
	// Multiply by the square root of the number of periods in a year
	// corresponding to the data frequency.
	annualizedVolatility := stdDev * math.Sqrt(annualizationFactor)

	return annualizedVolatility, nil
}

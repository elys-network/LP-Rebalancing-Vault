/*
This file contains common utility functions for converting between different types,
particularly for SDK math operations and precision handling.
*/

package utils

import (
	"errors"
	"fmt"
	"math"

	sdkmath "cosmossdk.io/math"
)

// Error definitions for zero-tolerance error handling
var (
	ErrInvalidPrecision = errors.New("precision is invalid")
	ErrAmountNil        = errors.New("amount is nil")
	ErrAmountNegative   = errors.New("amount is negative")
	ErrNotFinite        = errors.New("value is not finite")
	ErrConversionFailed = errors.New("conversion failed")
)

// SDKIntToFloat64 converts an SDK Int to float64 with proper precision handling
func SDKIntToFloat64(amount sdkmath.Int, precision int) (float64, error) {
	if precision < 0 || precision > 18 {
		return 0, fmt.Errorf("%w: %d (must be between 0 and 18)", ErrInvalidPrecision, precision)
	}
	if amount.IsNil() {
		return 0, ErrAmountNil
	}
	if amount.IsNegative() {
		return 0, ErrAmountNegative
	}

	decAmount := sdkmath.LegacyNewDecFromInt(amount)
	factor := sdkmath.LegacyNewDec(1)
	for i := 0; i < precision; i++ {
		factor = factor.Mul(sdkmath.LegacyNewDec(10))
	}

	result := decAmount.Quo(factor)
	resultFloat, err := result.Float64()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrConversionFailed, err)
	}

	if math.IsNaN(resultFloat) || math.IsInf(resultFloat, 0) {
		return 0, fmt.Errorf("%w: result is %f", ErrNotFinite, resultFloat)
	}

	return resultFloat, nil
}

// Float64ToSDKInt converts a float64 to SDK Int with proper precision handling
func Float64ToSDKInt(amount float64, precision int) (sdkmath.Int, error) {
	if precision < 0 || precision > 18 {
		return sdkmath.ZeroInt(), fmt.Errorf("%w: %d (must be between 0 and 18)", ErrInvalidPrecision, precision)
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return sdkmath.ZeroInt(), fmt.Errorf("%w: amount is %f", ErrNotFinite, amount)
	}
	if amount < 0 {
		return sdkmath.ZeroInt(), ErrAmountNegative
	}
	if amount == 0 {
		return sdkmath.ZeroInt(), nil
	}

	// Use string conversion to avoid floating point precision issues
	formatStr := fmt.Sprintf("%%.%df", precision)
	amountStr := fmt.Sprintf(formatStr, amount)

	decAmount, err := sdkmath.LegacyNewDecFromStr(amountStr)
	if err != nil {
		return sdkmath.ZeroInt(), fmt.Errorf("%w: failed to create decimal from string: %w", ErrConversionFailed, err)
	}

	factor := sdkmath.LegacyNewDec(1)
	for i := 0; i < precision; i++ {
		factor = factor.Mul(sdkmath.LegacyNewDec(10))
	}

	result := decAmount.Mul(factor).TruncateInt()
	if result.IsNegative() {
		return sdkmath.ZeroInt(), ErrAmountNegative
	}

	return result, nil
} 
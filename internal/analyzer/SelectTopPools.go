/*

This file contains the function for selecting the top pools based on the score from CalculatePoolScore.

*/

package analyzer

import (
	"errors"
	"fmt"
	"math" // For checking NaN/Inf scores
	"sort"

	"github.com/elys-network/avm/internal/logger"
	"github.com/elys-network/avm/internal/types" // Adjust import path as necessary
)

var poolSelectorLogger = logger.GetForComponent("pool_selector")
var ErrNoValidPools = errors.New("no pools with valid scores found")
var ErrInvalidAllocationConstraints = errors.New("invalid allocation constraints")
var ErrAllocationImpossible = errors.New("allocation constraints cannot be satisfied")

const maxAllocationIterations = 20 // Prevent potential infinite loops in constraint logic

// SelectTopPools filters and selects the highest-scoring pools based on the results
// from CalculatePoolScore and the MaxPools parameter.
// Returns error if no valid pools are found or constraints are invalid.
func SelectTopPools(scoredPools []types.PoolScoreResult, params types.ScoringParameters) ([]types.PoolID, error) {
	// Handle Empty Input
	if len(scoredPools) == 0 {
		poolSelectorLogger.Error().Msg("Input scoredPools slice is empty")
		return nil, errors.New("no pools provided for selection")
	}

	// Validate MaxPools parameter
	if params.MaxPools <= 0 {
		poolSelectorLogger.Error().
			Int("maxPools", params.MaxPools).
			Msg("MaxPools parameter must be positive")
		return nil, errors.New("MaxPools parameter must be positive")
	}

	// Filter out Pools with Invalid Scores (NaN or Infinity)
	validScoredPools := make([]types.PoolScoreResult, 0, len(scoredPools))
	for _, poolScore := range scoredPools {
		if math.IsNaN(poolScore.Score) || math.IsInf(poolScore.Score, 0) {
			poolSelectorLogger.Error().
				Uint64("poolID", uint64(poolScore.PoolID)).
				Float64("score", poolScore.Score).
				Msg("Pool has invalid score")
			return nil, fmt.Errorf("pool %d has invalid score: %f", poolScore.PoolID, poolScore.Score)
		}
		validScoredPools = append(validScoredPools, poolScore)
	}

	// Must have at least one valid pool
	if len(validScoredPools) == 0 {
		poolSelectorLogger.Error().Msg("No pools have valid scores")
		return nil, ErrNoValidPools
	}

	// Sort the Valid Pools by Score (Descending Order)
	sort.Slice(validScoredPools, func(i, j int) bool {
		return validScoredPools[i].Score > validScoredPools[j].Score
	})

	// Determine How Many Pools to Select
	numberOfPoolsToSelect := params.MaxPools
	if numberOfPoolsToSelect > len(validScoredPools) {
		numberOfPoolsToSelect = len(validScoredPools)
	}

	// Extract the PoolIDs of the Top N Pools
	selectedPoolIDs := make([]types.PoolID, numberOfPoolsToSelect)
	poolSelectorLogger.Info().
		Int("count", numberOfPoolsToSelect).
		Msg("Selecting top pools")

	for i := 0; i < numberOfPoolsToSelect; i++ {
		selectedPoolIDs[i] = validScoredPools[i].PoolID
		poolSelectorLogger.Debug().
			Int("rank", i+1).
			Uint64("poolID", uint64(validScoredPools[i].PoolID)).
			Float64("score", validScoredPools[i].Score).
			Msg("Selected pool")
	}

	return selectedPoolIDs, nil
}

// DetermineTargetAllocations calculates the target percentage allocation for each selected pool
// based on their scores, respecting Min/Max allocation constraints.
// Returns error if allocation is impossible or constraints are invalid.
func DetermineTargetAllocations(
	selectedPoolIDs []types.PoolID,
	scoredPoolsMap map[types.PoolID]types.PoolScoreResult,
	params types.ScoringParameters,
) (map[types.PoolID]float64, error) {

	// --- 1. Handle Edge Cases ---
	numSelected := len(selectedPoolIDs)
	if numSelected == 0 {
		poolSelectorLogger.Debug().Msg("No pools selected for allocation")
		return make(map[types.PoolID]float64), nil
	}

	// Strict validation of allocation constraints
	if math.IsNaN(params.MinAllocation) || math.IsInf(params.MinAllocation, 0) {
		return nil, errors.New("MinAllocation is not finite")
	}
	if math.IsNaN(params.MaxAllocation) || math.IsInf(params.MaxAllocation, 0) {
		return nil, errors.New("MaxAllocation is not finite")
	}
	if params.MinAllocation < 0 {
		return nil, errors.New("MinAllocation cannot be negative")
	}
	if params.MaxAllocation <= 0 {
		return nil, errors.New("MaxAllocation must be positive")
	}
	if params.MinAllocation > params.MaxAllocation {
		return nil, fmt.Errorf("MinAllocation (%.4f) cannot be greater than MaxAllocation (%.4f)", params.MinAllocation, params.MaxAllocation)
	}

	// Check if minimum constraints can be satisfied
	minTotalRequired := float64(numSelected) * params.MinAllocation
	if minTotalRequired > 1.00001 { // Add small tolerance for floating point
		return nil, fmt.Errorf("minimum allocation constraint (%.4f per pool) cannot be satisfied for %d pools: requires %.4f total",
			params.MinAllocation, numSelected, minTotalRequired)
	}

	// Check if equal distribution would violate max constraint
	equalShare := 1.0 / float64(numSelected)
	if equalShare > params.MaxAllocation {
		return nil, fmt.Errorf("equal distribution (%.4f per pool) violates MaxAllocation constraint (%.4f)",
			equalShare, params.MaxAllocation)
	}

	// --- 2. Validate All Pools Exist and Have Valid Scores ---
	type poolScoreInfo struct {
		ID    types.PoolID
		Score float64
	}
	validPools := make([]poolScoreInfo, 0, numSelected)
	var totalScore float64 = 0

	for _, id := range selectedPoolIDs {
		scoreResult, exists := scoredPoolsMap[id]
		if !exists {
			return nil, fmt.Errorf("score result not found for selected pool ID %d", id)
		}

		// Validate the score is finite
		if math.IsNaN(scoreResult.Score) || math.IsInf(scoreResult.Score, 0) {
			return nil, fmt.Errorf("pool %d has invalid score: %f", id, scoreResult.Score)
		}

		// All selected pools must have positive scores for meaningful allocation
		if scoreResult.Score <= 0 {
			return nil, fmt.Errorf("pool %d has non-positive score: %f - cannot allocate based on score", id, scoreResult.Score)
		}

		validPools = append(validPools, poolScoreInfo{ID: id, Score: scoreResult.Score})
		totalScore += scoreResult.Score
	}

	// Total score must be positive
	if totalScore <= 0 {
		return nil, errors.New("total score of selected pools is non-positive")
	}

	// --- 3. Calculate Initial Score-Based Allocations ---
	allocations := make(map[types.PoolID]float64)
	for _, p := range validPools {
		allocations[p.ID] = p.Score / totalScore
	}

	// --- 4. Iteratively Enforce Constraints ---
	lockedAllocations := make(map[types.PoolID]float64)  // Pools whose allocations are finalized
	unlockedPoolScores := make(map[types.PoolID]float64) // PoolID -> Score for pools still being adjusted
	for _, p := range validPools {
		unlockedPoolScores[p.ID] = p.Score
	}

	iteration := 0
	madeChanges := true

	for madeChanges && iteration < maxAllocationIterations {
		madeChanges = false
		iteration++
		poolSelectorLogger.Debug().
			Int("iteration", iteration).
			Msg("Allocation constraint enforcement iteration")

		// Calculate remaining allocation space
		remainingPercent := 1.0
		for _, lockedAlloc := range lockedAllocations {
			remainingPercent -= lockedAlloc
		}

		// Floating point safety
		if remainingPercent < -0.00001 {
			return nil, errors.New("allocation constraint enforcement resulted in over-allocation")
		}
		if remainingPercent < 0 {
			remainingPercent = 0
		}

		// Calculate total score of unlocked pools
		totalUnlockedScore := 0.0
		for _, score := range unlockedPoolScores {
			totalUnlockedScore += score
		}

		if len(unlockedPoolScores) == 0 {
			break // All pools are locked
		}

		if totalUnlockedScore <= 0 {
			return nil, errors.New("total unlocked score is non-positive during constraint enforcement")
		}

		poolsToLock := []types.PoolID{}

		// Check constraints for each unlocked pool
		for id, score := range unlockedPoolScores {
			// Calculate proportional allocation
			currentAlloc := (score / totalUnlockedScore) * remainingPercent
			allocations[id] = currentAlloc

			// Check constraints
			if currentAlloc < params.MinAllocation {
				poolSelectorLogger.Debug().
					Uint64("poolID", uint64(id)).
					Float64("currentAllocation", currentAlloc).
					Float64("minAllocation", params.MinAllocation).
					Msg("Pool below min allocation. Locking at min")
				lockedAllocations[id] = params.MinAllocation
				poolsToLock = append(poolsToLock, id)
				madeChanges = true
			} else if currentAlloc > params.MaxAllocation {
				poolSelectorLogger.Debug().
					Uint64("poolID", uint64(id)).
					Float64("currentAllocation", currentAlloc).
					Float64("maxAllocation", params.MaxAllocation).
					Msg("Pool above max allocation. Locking at max")
				lockedAllocations[id] = params.MaxAllocation
				poolsToLock = append(poolsToLock, id)
				madeChanges = true
			}
		}

		// Remove newly locked pools from unlocked set
		for _, id := range poolsToLock {
			delete(unlockedPoolScores, id)
		}
	}

	if iteration == maxAllocationIterations && madeChanges {
		return nil, fmt.Errorf("allocation constraint enforcement failed to converge after %d iterations", maxAllocationIterations)
	}

	// --- 5. Final Assignment and Validation ---
	targetAllocations := make(map[types.PoolID]float64)
	finalSum := 0.0

	// Assign final allocations
	for _, id := range selectedPoolIDs {
		if lockedVal, isLocked := lockedAllocations[id]; isLocked {
			targetAllocations[id] = lockedVal
		} else if unlocked, exists := allocations[id]; exists {
			targetAllocations[id] = unlocked
		} else {
			return nil, fmt.Errorf("allocation not found for pool %d", id)
		}
		finalSum += targetAllocations[id]
	}

	// Validate final allocation sum
	if math.Abs(finalSum-1.0) > 0.001 { // Allow small tolerance for floating point
		return nil, fmt.Errorf("final allocation sum (%.6f) deviates significantly from 1.0", finalSum)
	}

	// Normalize to exactly 1.0
	if finalSum > 0 {
		scaleFactor := 1.0 / finalSum
		for id := range targetAllocations {
			targetAllocations[id] *= scaleFactor
		}
	} else {
		return nil, errors.New("final allocation sum is zero")
	}

	// Final validation - check all constraints are satisfied
	for id, alloc := range targetAllocations {
		if alloc < params.MinAllocation-0.00001 || alloc > params.MaxAllocation+0.00001 {
			return nil, fmt.Errorf("final allocation for pool %d (%.6f) violates constraints [%.4f, %.4f]",
				id, alloc, params.MinAllocation, params.MaxAllocation)
		}
	}

	// Log final allocations
	poolSelectorLogger.Info().Msg("Final target allocations calculated")
	for id, alloc := range targetAllocations {
		poolSelectorLogger.Info().
			Uint64("poolID", uint64(id)).
			Float64("allocation", alloc*100).
			Msg("Pool allocation percentage")
	}

	return targetAllocations, nil
}

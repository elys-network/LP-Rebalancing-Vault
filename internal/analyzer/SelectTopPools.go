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
// Ensures that a pool containing ELYS (uelys denom) is always included for forced allocation.
// Returns the selected pool IDs and the ID of the ELYS pool (0 if not found).
func SelectTopPools(scoredPools []types.PoolScoreResult, params types.ScoringParameters, poolsDataMap map[types.PoolID]types.Pool) ([]types.PoolID, types.PoolID, error) {
	// Handle Empty Input
	if len(scoredPools) == 0 {
		poolSelectorLogger.Error().Msg("Input scoredPools slice is empty")
		return nil, 0, errors.New("no pools provided for selection")
	}

	// Validate MaxPools parameter
	if params.MaxPools <= 0 {
		poolSelectorLogger.Error().
			Int("maxPools", params.MaxPools).
			Msg("MaxPools parameter must be positive")
		return nil, 0, errors.New("MaxPools parameter must be positive")
	}

	// Filter out Pools with Invalid Scores (NaN or Infinity)
	validScoredPools := make([]types.PoolScoreResult, 0, len(scoredPools))
	var elysPoolID types.PoolID = 0
	var elysPoolFound bool = false

	for _, poolScore := range scoredPools {
		if math.IsNaN(poolScore.Score) || math.IsInf(poolScore.Score, 0) {
			poolSelectorLogger.Error().
				Uint64("poolID", uint64(poolScore.PoolID)).
				Float64("score", poolScore.Score).
				Msg("Pool has invalid score")
			return nil, 0, fmt.Errorf("pool %d has invalid score: %f", poolScore.PoolID, poolScore.Score)
		}
		validScoredPools = append(validScoredPools, poolScore)

		// Check if this pool contains ELYS (uelys denom)
		if poolData, exists := poolsDataMap[poolScore.PoolID]; exists {
			if poolData.TokenA.Denom == "uelys" || poolData.TokenB.Denom == "uelys" {
				if !elysPoolFound {
					// First ELYS pool found
					elysPoolID = poolScore.PoolID
					elysPoolFound = true
				} else {
					// Compare with current best ELYS pool score
					for _, existingPoolScore := range scoredPools {
						if existingPoolScore.PoolID == elysPoolID && poolScore.Score > existingPoolScore.Score {
							// This ELYS pool has a better score
							elysPoolID = poolScore.PoolID
							break
						}
					}
				}
			}
		}
	}

	// Must have at least one valid pool
	if len(validScoredPools) == 0 {
		poolSelectorLogger.Error().Msg("No pools have valid scores")
		return nil, 0, ErrNoValidPools
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

	// Create initial selection of top N pools
	selectedPoolsMap := make(map[types.PoolID]bool)
	selectedPoolsList := make([]types.PoolID, 0, numberOfPoolsToSelect)

	for i := 0; i < numberOfPoolsToSelect; i++ {
		poolID := validScoredPools[i].PoolID
		selectedPoolsMap[poolID] = true
		selectedPoolsList = append(selectedPoolsList, poolID)
		poolSelectorLogger.Debug().
			Int("rank", i+1).
			Uint64("poolID", uint64(poolID)).
			Float64("score", validScoredPools[i].Score).
			Msg("Initially selected pool")
	}

	// Ensure ELYS pool is included (force inclusion if not already selected)
	if elysPoolFound && !selectedPoolsMap[elysPoolID] {
		// Remove the lowest-scoring pool from initial selection
		if len(selectedPoolsList) > 0 {
			lowestScoringPoolID := selectedPoolsList[len(selectedPoolsList)-1]
			delete(selectedPoolsMap, lowestScoringPoolID)
			selectedPoolsList = selectedPoolsList[:len(selectedPoolsList)-1]

			poolSelectorLogger.Warn().
				Uint64("elysPoolID", uint64(elysPoolID)).
				Uint64("replacedPoolID", uint64(lowestScoringPoolID)).
				Msg("ELYS pool not in top selection; replacing lowest-scoring pool")
		}

		// Add ELYS pool to selection
		selectedPoolsMap[elysPoolID] = true
		selectedPoolsList = append(selectedPoolsList, elysPoolID)
	}

	if !elysPoolFound {
		poolSelectorLogger.Warn().Msg("No ELYS pools found in available pools - forced allocation will not apply")
	}

	poolSelectorLogger.Info().
		Int("count", len(selectedPoolsList)).
		Uint64("elysPoolID", uint64(elysPoolID)).
		Bool("elysPoolIncluded", elysPoolFound).
		Msg("Pool selection complete with ELYS pool enforcement")

	return selectedPoolsList, elysPoolID, nil
}

// DetermineTargetAllocations calculates the target percentage allocation for each selected pool
// based on their scores, respecting Min/Max allocation constraints and ensuring ELYS pools
// receive at least the minimum forced allocation.
// Returns error if allocation is impossible or constraints are invalid.
func DetermineTargetAllocations(
	selectedPoolIDs []types.PoolID,
	scoredPoolsMap map[types.PoolID]types.PoolScoreResult,
	params types.ScoringParameters,
	elysPoolID types.PoolID, // ID of the ELYS pool that requires minimum allocation (0 if none)
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

	// Validate ELYS forced allocation parameter
	if math.IsNaN(params.ElysForcedAllocationMinimum) || math.IsInf(params.ElysForcedAllocationMinimum, 0) {
		return nil, errors.New("ElysForcedAllocationMinimum is not finite")
	}
	if params.ElysForcedAllocationMinimum < 0 || params.ElysForcedAllocationMinimum > 1 {
		return nil, fmt.Errorf("ElysForcedAllocationMinimum (%.4f) must be between 0 and 1", params.ElysForcedAllocationMinimum)
	}

	// Check if minimum constraints can be satisfied with ELYS forced allocation
	elysPoolInSelection := false
	for _, poolID := range selectedPoolIDs {
		if poolID == elysPoolID {
			elysPoolInSelection = true
			break
		}
	}

	minTotalRequired := 0.0
	if elysPoolInSelection && elysPoolID != 0 {
		// ELYS pool gets its forced minimum
		minTotalRequired += params.ElysForcedAllocationMinimum
		// Other pools get the standard minimum
		minTotalRequired += float64(numSelected-1) * params.MinAllocation
	} else {
		// No ELYS pool, all pools get standard minimum
		minTotalRequired = float64(numSelected) * params.MinAllocation
	}

	if minTotalRequired > 1.00001 { // Add small tolerance for floating point
		return nil, fmt.Errorf("minimum allocation constraints cannot be satisfied for %d pools: requires %.4f total (ELYS minimum: %.4f)",
			numSelected, minTotalRequired, params.ElysForcedAllocationMinimum)
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

	// --- 4. Iteratively Enforce Constraints (Including ELYS Minimum) ---
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

			// Determine the appropriate minimum allocation for this pool
			minAlloc := params.MinAllocation
			if id == elysPoolID && elysPoolID != 0 {
				// This is the ELYS pool - use the forced minimum
				minAlloc = params.ElysForcedAllocationMinimum
			}

			// Check constraints
			if currentAlloc < minAlloc {
				poolSelectorLogger.Debug().
					Uint64("poolID", uint64(id)).
					Float64("currentAllocation", currentAlloc).
					Float64("requiredMinimum", minAlloc).
					Bool("isElysPool", id == elysPoolID).
					Msg("Pool below minimum allocation. Locking at minimum")
				lockedAllocations[id] = minAlloc
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

	// Final validation - check all constraints are satisfied including ELYS minimum
	for id, alloc := range targetAllocations {
		minRequired := params.MinAllocation
		if id == elysPoolID && elysPoolID != 0 {
			minRequired = params.ElysForcedAllocationMinimum
		}

		if alloc < minRequired-0.00001 || alloc > params.MaxAllocation+0.00001 {
			return nil, fmt.Errorf("final allocation for pool %d (%.6f) violates constraints [%.4f, %.4f]",
				id, alloc, minRequired, params.MaxAllocation)
		}
	}

	// Log final allocations with ELYS pool highlighting
	poolSelectorLogger.Info().Msg("Final target allocations calculated")
	for id, alloc := range targetAllocations {
		isElysPpool := id == elysPoolID && elysPoolID != 0
		poolSelectorLogger.Info().
			Uint64("poolID", uint64(id)).
			Float64("allocation", alloc*100).
			Bool("isElysPool", isElysPpool).
			Msg("Pool allocation percentage")
	}

	return targetAllocations, nil
}

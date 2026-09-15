// Package coins provides checked coin accumulation for vesting schedules.
package coins

import (
	"sort"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Accumulator folds consecutive coin sets without repeatedly copying and
// sorting a growing total. Its zero value is ready for use. Add preserves
// Coins.Add's checked arithmetic and nondecreasing input-denomination contract.
type Accumulator struct {
	amounts sdk.Coins
	index   map[string]int
}

// Add accumulates a single sorted coin set. Duplicate denominations, zero and
// negative amounts have the same arithmetic behavior as sdk.Coins.Add.
func (a *Accumulator) Add(coins sdk.Coins) {
	if !sort.IsSorted(coins) {
		panic("Wrong argument: coins must be sorted")
	}
	if a.index == nil {
		a.index = make(map[string]int)
		a.amounts = sdk.Coins{}
	}
	for _, coin := range coins {
		if index, exists := a.index[coin.Denom]; exists {
			a.amounts[index].Amount = a.amounts[index].Amount.Add(coin.Amount)
		} else {
			a.index[coin.Denom] = len(a.amounts)
			// Adding to zero copies the amount and preserves nil/overflow
			// failures instead of aliasing the caller's stored schedule.
			a.amounts = append(a.amounts, sdk.Coin{Denom: coin.Denom, Amount: math.ZeroInt().Add(coin.Amount)})
		}
	}
}

// Coins returns the canonical nonzero total. It preserves nil before the first
// Add and a non-nil empty result after an Add with no nonzero total.
func (a Accumulator) Coins() sdk.Coins {
	if a.amounts == nil {
		return nil
	}
	total := make(sdk.Coins, 0, len(a.amounts))
	for _, coin := range a.amounts {
		if !coin.IsZero() {
			total = append(total, coin)
		}
	}
	return total.Sort()
}

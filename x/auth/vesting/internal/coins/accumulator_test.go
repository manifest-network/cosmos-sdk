package coins_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting/internal/coins"
)

func TestAccumulatorZeroValueCoins(t *testing.T) {
	var accumulator coins.Accumulator
	require.Nil(t, accumulator.Coins())
}

func TestAccumulatorAddNilCoins(t *testing.T) {
	var accumulator coins.Accumulator
	accumulator.Add(nil)
	require.Equal(t, sdk.Coins{}, accumulator.Coins())
}

func TestAccumulatorAddRejectsUnsortedCoins(t *testing.T) {
	var accumulator coins.Accumulator
	unsorted := sdk.Coins{
		sdk.NewInt64Coin("uzzz", 1),
		sdk.NewInt64Coin("uaaa", 1),
	}
	require.PanicsWithValue(t, "Wrong argument: coins must be sorted", func() {
		accumulator.Add(unsorted)
	})
}

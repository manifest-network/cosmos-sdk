package types_test

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
)

// Keep the prior algorithm as a differential oracle, including its shortcuts,
// nil/empty result shapes, checked math and malformed-input panic behavior.
func priorPeriodicVestedCoins(account types.PeriodicVestingAccount, at time.Time) sdk.Coins {
	var vested sdk.Coins
	if at.Unix() <= account.StartTime {
		return vested
	} else if at.Unix() >= account.EndTime {
		return account.OriginalVesting
	}
	start := account.StartTime
	for _, period := range account.VestingPeriods {
		if at.Unix()-start < period.Length {
			break
		}
		vested = vested.Add(period.Amount...)
		start += period.Length
	}
	return vested
}

func captureVestingResult(fn func() sdk.Coins) (coins sdk.Coins, panicValue any) {
	defer func() { panicValue = recover() }()
	return fn(), nil
}

func TestPeriodicVestingAccumulationMatchesPreviousBehavior(t *testing.T) {
	maximum := math.NewIntFromBigInt(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)))
	coin := func(denom string, amount int64) sdk.Coin { return sdk.Coin{Denom: denom, Amount: math.NewInt(amount)} }
	for _, tc := range []struct {
		name    string
		periods types.Periods
	}{
		{"empty schedule", nil},
		{"empty periods", types.Periods{{Length: 1}, {Length: 2, Amount: sdk.Coins{}}}},
		{"multiple denominations", types.Periods{{Length: 2, Amount: sdk.Coins{coin("ubeta", 3)}}, {Length: 1, Amount: sdk.Coins{coin("ualpha", 7), coin("ubeta", 9)}}}},
		{"zero length", types.Periods{{Amount: sdk.Coins{coin("umfx", 2)}}, {Length: 2, Amount: sdk.Coins{coin("umfx", 3)}}}},
		{"negative length", types.Periods{{Length: -1, Amount: sdk.Coins{coin("umfx", 2)}}, {Length: 2, Amount: sdk.Coins{coin("umfx", 3)}}}},
		{"duplicate denomination", types.Periods{{Length: 1, Amount: sdk.Coins{coin("umfx", 2), coin("umfx", 3)}}}},
		{"unsorted denomination", types.Periods{{Length: 1, Amount: sdk.Coins{coin("uzzz", 2), coin("uaaa", 3)}}}},
		{"negative cancellation", types.Periods{{Length: 1, Amount: sdk.Coins{coin("umfx", 2)}}, {Length: 1, Amount: sdk.Coins{coin("umfx", -2)}}, {Length: 1, Amount: sdk.Coins{coin("umfx", 4)}}}},
		{"zero amount", types.Periods{{Length: 1, Amount: sdk.Coins{coin("umfx", 0)}}}},
		{"nil amount", types.Periods{{Length: 1, Amount: sdk.Coins{{Denom: "umfx"}}}}},
		{"overflow across periods", types.Periods{{Length: 1, Amount: sdk.Coins{{Denom: "umfx", Amount: maximum}}}, {Length: 1, Amount: sdk.Coins{coin("umfx", 1)}}}},
		{"overflow within period", types.Periods{{Length: 1, Amount: sdk.Coins{{Denom: "umfx", Amount: maximum}, coin("umfx", 1)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// TotalAmount exercises the same schedule accumulation used during
			// validation and message creation, including malformed inputs.
			wantTotal, wantPanic := captureVestingResult(func() sdk.Coins {
				total := sdk.Coins{}
				for _, period := range tc.periods {
					total = total.Add(period.Amount...)
				}
				return total
			})
			gotTotal, gotPanic := captureVestingResult(tc.periods.TotalAmount)
			require.Equal(t, fmt.Sprintf("%T:%v", wantPanic, wantPanic), fmt.Sprintf("%T:%v", gotPanic, gotPanic))
			require.Equal(t, wantTotal, gotTotal)
			account := types.NewPeriodicVestingAccountRaw(&types.BaseVestingAccount{
				EndTime: 110, OriginalVesting: sdk.NewCoins(coin("umfx", 100)),
			}, 100, tc.periods)
			for _, unix := range []int64{99, 100, 101, 102, 103, 109, 110, 111} {
				at := time.Unix(unix, 500_000_000)
				want, wantPanic := captureVestingResult(func() sdk.Coins { return priorPeriodicVestedCoins(*account, at) })
				got, gotPanic := captureVestingResult(func() sdk.Coins { return account.GetVestedCoins(at) })
				require.Equal(t, fmt.Sprintf("%T:%v", wantPanic, wantPanic), fmt.Sprintf("%T:%v", gotPanic, gotPanic), "time %d", unix)
				require.Equal(t, want, got, "time %d", unix)
			}
		})
	}
}

func TestPeriodicVestingAccumulationDoesNotAliasSchedule(t *testing.T) {
	account := types.NewPeriodicVestingAccountRaw(&types.BaseVestingAccount{
		EndTime: 110, OriginalVesting: sdk.NewCoins(sdk.NewInt64Coin("umfx", 5)),
	}, 100, types.Periods{{Length: 1, Amount: sdk.NewCoins(sdk.NewInt64Coin("umfx", 2))}, {Length: 9, Amount: sdk.NewCoins(sdk.NewInt64Coin("umfx", 3))}})
	result := account.GetVestedCoins(time.Unix(105, 0))
	result[0].Amount.BigIntMut().SetInt64(999)
	require.Equal(t, int64(2), account.VestingPeriods[0].Amount[0].Amount.Int64())
	require.Equal(t, int64(5), account.OriginalVesting[0].Amount.Int64())
}

func FuzzPeriodicVestingAccumulationMatchesPreviousBehavior(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{255, 128, 254, 1, 250, 8, 14})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		if len(data) > 32 {
			data = data[:32]
		}
		periods := make(types.Periods, len(data))
		original := sdk.Coins{}
		end := int64(100)
		for index, value := range data {
			length := int64(value%4 + 1)
			amount := sdk.NewCoins(sdk.NewInt64Coin(fmt.Sprintf("udenom%d", value%7), int64(value)+1))
			periods[index] = types.Period{Length: length, Amount: amount}
			end += length
			original = original.Add(amount...)
		}
		account := types.NewPeriodicVestingAccountRaw(&types.BaseVestingAccount{EndTime: end, OriginalVesting: original}, 100, periods)
		for _, unix := range []int64{99, 100, 101, (100 + end) / 2, end - 1, end, end + 1} {
			at := time.Unix(unix, 500_000_000)
			require.Equal(t, priorPeriodicVestedCoins(*account, at), account.GetVestedCoins(at))
		}
	})
}

func BenchmarkPeriodicVestingAccumulation(b *testing.B) {
	for _, count := range []int{1_000, 2_000} {
		b.Run(fmt.Sprintf("denominations=%d", count), func(b *testing.B) {
			periods := make(types.Periods, count)
			original := make(sdk.Coins, count)
			for index := range periods {
				coin := sdk.NewInt64Coin(fmt.Sprintf("udenom%06d", index), 1)
				periods[index] = types.Period{Length: 1, Amount: sdk.Coins{coin}}
				original[index] = coin
			}
			account := types.NewPeriodicVestingAccountRaw(&types.BaseVestingAccount{
				EndTime: 100 + int64(count), OriginalVesting: original,
			}, 100, periods)
			at := time.Unix(account.EndTime-1, 0)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(account.Size()), "account-bytes")
			for i := 0; i < b.N; i++ {
				if len(account.LockedCoins(at)) != 1 {
					b.Fatal("expected only the last period to remain locked")
				}
			}
		})
	}
}

// Validate is called for imports and again by the transaction constructor.
func BenchmarkPeriodicVestingValidation(b *testing.B) {
	for _, count := range []int{1_000, 2_000, 10_000} {
		b.Run(fmt.Sprintf("denominations=%d", count), func(b *testing.B) {
			periods := make(types.Periods, count)
			original := make(sdk.Coins, count)
			for index := range periods {
				coin := sdk.NewInt64Coin(fmt.Sprintf("udenom%06d", index), 1)
				periods[index] = types.Period{Length: 1, Amount: sdk.Coins{coin}}
				original[index] = coin
			}
			account := types.NewPeriodicVestingAccountRaw(&types.BaseVestingAccount{
				BaseAccount: authtypes.NewBaseAccountWithAddress(sdk.AccAddress(make([]byte, 20))),
				EndTime:     100 + int64(count), OriginalVesting: original,
			}, 100, periods)
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(account.Size()), "account-bytes")
			for i := 0; i < b.N; i++ {
				if err := account.Validate(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

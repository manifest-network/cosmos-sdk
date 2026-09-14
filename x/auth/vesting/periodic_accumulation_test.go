package vesting_test

import (
	"fmt"

	"github.com/golang/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	vestingtypes "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
)

func (s *VestingTestSuite) TestCreatePeriodicVestingAccountManyDenominations() {
	const count = 2_000
	periods := make([]vestingtypes.Period, count)
	original := make(sdk.Coins, count)
	for index := range periods {
		coin := sdk.NewInt64Coin(fmt.Sprintf("udenom%06d", index), 1)
		periods[index] = vestingtypes.Period{Length: 1, Amount: sdk.Coins{coin}}
		original[index] = coin
	}
	s.bankKeeper.EXPECT().BlockedAddr(to1Addr).Return(false)
	sendEnabledArgs := []interface{}{gomock.Any()}
	for _, coin := range original {
		sendEnabledArgs = append(sendEnabledArgs, coin)
	}
	s.bankKeeper.EXPECT().IsSendEnabledCoins(sendEnabledArgs[0], sendEnabledArgs[1:]...).Return(nil)
	s.bankKeeper.EXPECT().SendCoins(gomock.Any(), fromAddr, to1Addr, original).Return(nil)
	response, err := s.msgServer.CreatePeriodicVestingAccount(s.ctx, &vestingtypes.MsgCreatePeriodicVestingAccount{
		FromAddress: fromAddr.String(), ToAddress: to1Addr.String(), StartTime: 100, VestingPeriods: periods,
	})
	s.Require().NoError(err)
	s.Require().NotNil(response)
	account, ok := s.accountKeeper.GetAccount(s.ctx, to1Addr).(*vestingtypes.PeriodicVestingAccount)
	s.Require().True(ok)
	s.Require().Equal(original, account.OriginalVesting)
	s.Require().Equal(periods, account.VestingPeriods)
	s.Require().Equal(int64(100+count), account.EndTime)
	s.Require().NoError(account.Validate())
}

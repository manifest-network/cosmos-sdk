package server

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	cmtdb "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestSaveTestnetCommitReconstructsConsensus(t *testing.T) {
	for _, extensionsEnabled := range []bool{false, true} {
		for _, oldFlag := range []cmttypes.BlockIDFlag{cmttypes.BlockIDFlagAbsent, cmttypes.BlockIDFlagNil, cmttypes.BlockIDFlagCommit} {
			t.Run(fmt.Sprintf("extensions=%v/oldFlag=%v", extensionsEnabled, oldFlag), func(t *testing.T) {
				db := cmtdb.NewMemDB()
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				vote, validators := signedTestnetVote(t)

				// Seed stale, incompatible commit records. Conversion must construct
				// a complete new signature regardless of the old first signature.
				stale := &cmttypes.ExtendedCommit{
					Height: vote.Height, Round: 7, BlockID: vote.BlockID,
					ExtendedSignatures: []cmttypes.ExtendedCommitSig{{CommitSig: cmttypes.CommitSig{BlockIDFlag: oldFlag}}},
				}
				staleBytes, err := stale.ToProto().Marshal()
				require.NoError(t, err)
				require.NoError(t, db.Set([]byte("EC:42"), staleBytes))
				seenBytes, err := stale.ToCommit().ToProto().Marshal()
				require.NoError(t, err)
				require.NoError(t, db.Set([]byte("SC:42"), seenBytes))

				require.NoError(t, saveTestnetCommit(db, vote, extensionsEnabled))
				blockStore := store.NewBlockStore(db)
				seen := blockStore.LoadSeenCommit(vote.Height)
				require.Len(t, seen.Signatures, 1)
				require.Equal(t, cmttypes.BlockIDFlagCommit, seen.Signatures[0].BlockIDFlag)
				require.True(t, seen.ToVoteSet("testnet-chain", validators).HasTwoThirdsMajority())

				extended := blockStore.LoadBlockExtendedCommit(vote.Height)
				if extensionsEnabled {
					require.NotNil(t, extended)
					require.NoError(t, extended.EnsureExtensions(true))
					require.Len(t, extended.ExtendedSignatures, 1)
					require.Empty(t, extended.ExtendedSignatures[0].Extension)
					require.NotEmpty(t, extended.ExtendedSignatures[0].ExtensionSignature)
					require.True(t, extended.ToExtendedVoteSet("testnet-chain", validators).HasTwoThirdsMajority())
				} else {
					require.Nil(t, extended)
				}
			})
		}
	}
}

func TestSaveTestnetCommitRejectsInvalidVoteWithoutWriting(t *testing.T) {
	for _, missingBlockID := range []bool{false, true} {
		t.Run(fmt.Sprintf("missingBlockID=%v", missingBlockID), func(t *testing.T) {
			db := cmtdb.NewMemDB()
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			vote, _ := signedTestnetVote(t)
			require.NoError(t, db.Set([]byte("SC:42"), []byte("original seen commit")))
			require.NoError(t, db.Set([]byte("EC:42"), []byte("original extended commit")))
			if missingBlockID {
				vote.BlockID = cmttypes.BlockID{}
			} else {
				vote.ExtensionSignature = nil
			}
			require.Error(t, saveTestnetCommit(db, vote, true))
			seen, err := db.Get([]byte("SC:42"))
			require.NoError(t, err)
			require.Equal(t, []byte("original seen commit"), seen)
			extended, err := db.Get([]byte("EC:42"))
			require.NoError(t, err)
			require.Equal(t, []byte("original extended commit"), extended)
		})
	}
}

func signedTestnetVote(t *testing.T) (*cmttypes.Vote, *cmttypes.ValidatorSet) {
	t.Helper()
	home := t.TempDir()
	pv := privval.GenFilePV(filepath.Join(home, "key.json"), filepath.Join(home, "state.json"))
	pubKey, err := pv.GetPubKey()
	require.NoError(t, err)
	vote := &cmttypes.Vote{
		Type: cmtproto.PrecommitType, Height: 42, Round: 0, Timestamp: time.Now(),
		ValidatorAddress: pubKey.Address(), ValidatorIndex: 0,
		BlockID: cmttypes.BlockID{
			Hash:          bytes.Repeat([]byte{1}, 32),
			PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{2}, 32)},
		},
	}
	protoVote := vote.ToProto()
	require.NoError(t, pv.SignVote("testnet-chain", protoVote))
	vote, err = cmttypes.VoteFromProto(protoVote)
	require.NoError(t, err)
	return vote, cmttypes.NewValidatorSet([]*cmttypes.Validator{cmttypes.NewValidator(pubKey, 900000000000000)})
}

package server

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	cmtdb "github.com/cometbft/cometbft-db"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/evidence"
	cmtstate "github.com/cometbft/cometbft/proto/tendermint/state"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestTestnetifyClearsPendingEvidencePreservingCommitted(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.ctx.Config})
	require.NoError(t, err)
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: f.ctx.Config})
	require.NoError(t, err)
	evidenceDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "evidence", Config: f.ctx.Config})
	require.NoError(t, err)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
	pool, err := evidence.NewPool(evidenceDB, stateStore, store.NewBlockStore(blockDB))
	require.NoError(t, err)
	committed, err := cmttypes.NewMockDuplicateVoteEvidenceWithValidator(3, f.blockTimes[3], f.sourceSigner, "source-chain")
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(committed))
	state, err := stateStore.Load()
	require.NoError(t, err)
	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(time.Second)
	pool.Update(state, cmttypes.EvidenceList{committed})
	committedRecords := testnetDBContents(t, evidenceDB)
	require.Len(t, committedRecords, 1)
	// Seed through Comet's pool rather than copying its private key format.
	// This exercises the real v0.38.12 pending/committed prefix distinction.
	pending, err := cmttypes.NewMockDuplicateVoteEvidenceWithValidator(3, f.blockTimes[3], f.sourceSigner, "source-chain")
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(pending))
	proposalEvidence, _ := pool.PendingEvidence(-1)
	require.Len(t, proposalEvidence, 1)
	require.Equal(t, pending.Hash(), proposalEvidence[0].Hash())
	require.Len(t, testnetDBContents(t, evidenceDB), 2)
	require.NoError(t, pool.Close())
	require.NoError(t, stateDB.Close())
	require.NoError(t, blockDB.Close())

	_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
	require.NoError(t, err)
	evidenceDB = openTestnetDB(t, f.ctx.Config, "evidence")
	require.Equal(t, committedRecords, testnetDBContents(t, evidenceDB))
	pool, err = evidence.NewPool(evidenceDB,
		sm.NewStore(openTestnetDB(t, f.ctx.Config, "state"), sm.StoreOptions{}),
		store.NewBlockStore(openTestnetDB(t, f.ctx.Config, "blockstore")))
	require.NoError(t, err)
	proposalEvidence, _ = pool.PendingEvidence(-1)
	require.Empty(t, proposalEvidence)
	// Comet must still reject evidence already committed on the source chain.
	require.ErrorContains(t, pool.CheckEvidence(cmttypes.EvidenceList{committed}), "already committed")
	proposalEvidence, _ = pool.PendingEvidence(-1)
	require.Empty(t, proposalEvidence)
}

func TestTestnetifyReplacesReadOnlyAddrbook(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	path := filepath.Join(f.ctx.Config.RootDir, "config", "addrbook.json")
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o400))
	oldFile, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, oldFile.Close()) })
	_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
	require.NoError(t, err)
	replacement, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(replacement))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	// Holding the old file open also proves replacement when tests run with
	// privileges that would otherwise allow overwriting a read-only file.
	originalAfter, err := io.ReadAll(oldFile)
	require.NoError(t, err)
	require.Equal(t, original, originalAfter)
}

func TestRemoveTestnetWALPreservesMatchingDirectories(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"wal", "wal.0", "wal.42"} {
		require.NoError(t, os.Mkdir(filepath.Join(directory, name), 0o700))
	}
	child := filepath.Join(directory, "wal.0", "keep")
	require.NoError(t, os.WriteFile(child, []byte("unrelated directory contents"), 0o600))
	rotation := filepath.Join(directory, "wal.1")
	require.NoError(t, os.WriteFile(rotation, []byte("source WAL rotation"), 0o600))
	require.NoError(t, removeTestnetWAL(filepath.Join(directory, "wal")))
	for _, name := range []string{"wal", "wal.0", "wal.42"} {
		info, err := os.Stat(filepath.Join(directory, name))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	}
	data, err := os.ReadFile(child)
	require.NoError(t, err)
	require.Equal(t, []byte("unrelated directory contents"), data)
	_, err = os.Stat(rotation)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLoadTestnetLastFinalizeBlockResponseSupportsModernAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name string
		info cmtstate.ABCIResponsesInfo
	}{
		{name: "modern", info: cmtstate.ABCIResponsesInfo{Height: 7, ResponseFinalizeBlock: &abci.ResponseFinalizeBlock{AppHash: []byte("committed hash")}}},
		{name: "legacy without begin or end block", info: cmtstate.ABCIResponsesInfo{Height: 7, LegacyAbciResponses: &cmtstate.LegacyABCIResponses{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := cmtdb.NewMemDB()
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			data, err := tc.info.Marshal()
			require.NoError(t, err)
			require.NoError(t, db.SetSync(testnetLastABCIResponseKey(), data))
			response, err := loadTestnetLastFinalizeBlockResponse(db, sm.NewStore(db, sm.StoreOptions{}), tc.info.Height)
			require.NoError(t, err)
			require.NotNil(t, response)
			if tc.info.ResponseFinalizeBlock != nil {
				require.Equal(t, tc.info.ResponseFinalizeBlock, response)
			} else {
				require.Empty(t, response.TxResults)
				require.Empty(t, response.Events)
			}
		})
	}
}

func TestTestnetifyRejectsInvalidLastFinalizeResponse(t *testing.T) {
	const scenarioVariable = "SDK_TESTNET_INVALID_RESPONSE_SCENARIO"
	if scenario := os.Getenv(scenarioVariable); scenario != "" {
		f := newTestnetStateFixture(t, 3, 4, 4, true)
		db, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.ctx.Config})
		require.NoError(t, err)
		switch scenario {
		case "missing":
			require.NoError(t, db.DeleteSync(testnetLastABCIResponseKey()))
		case "empty bytes":
			require.NoError(t, db.SetSync(testnetLastABCIResponseKey(), []byte{}))
		case "malformed proto":
			require.NoError(t, db.SetSync(testnetLastABCIResponseKey(), []byte{0xff}))
		case "empty response payload":
			info := cmtstate.ABCIResponsesInfo{Height: 4}
			data, err := info.Marshal()
			require.NoError(t, err)
			require.NoError(t, db.SetSync(testnetLastABCIResponseKey(), data))
		default:
			t.Fatalf("unexpected response scenario %q", scenario)
		}
		require.NoError(t, db.Close())
		filesBefore := snapshotTestnetFiles(t, f)
		storesBefore := snapshotTestnetStores(t, f.ctx.Config)
		_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
		require.Error(t, err)
		require.True(t, f.creatorCalled, "the app must report its committed height before halt recovery")
		require.Equal(t, filesBefore, snapshotTestnetFiles(t, f))
		require.Equal(t, storesBefore, snapshotTestnetStores(t, f.ctx.Config))
		return
	}
	// Comet's unguarded loader exits on malformed proto and panics on an empty
	// response payload. Run each case in a child so a regression cannot abort
	// the surrounding suite, and require normal test completion in that child.
	for _, scenario := range []string{"missing", "empty bytes", "malformed proto", "empty response payload"} {
		t.Run(scenario, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestTestnetifyRejectsInvalidLastFinalizeResponse$", "-test.v")
			command.Env = append(os.Environ(), scenarioVariable+"="+scenario)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Contains(t, string(output), "--- PASS: TestTestnetifyRejectsInvalidLastFinalizeResponse")
		})
	}
}

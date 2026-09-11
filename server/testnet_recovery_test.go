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
	committed, committedRecords := seedTestnetEvidence(t, f)
	_, err := testnetify(f.ctx, f.creator, f.appDB, nil)
	require.NoError(t, err)
	evidenceDB := openTestnetDB(t, f.ctx.Config, "evidence")
	require.Equal(t, committedRecords, testnetDBContents(t, evidenceDB))
	pool, err := evidence.NewPool(evidenceDB,
		sm.NewStore(openTestnetDB(t, f.ctx.Config, "state"), sm.StoreOptions{}),
		store.NewBlockStore(openTestnetDB(t, f.ctx.Config, "blockstore")))
	require.NoError(t, err)
	proposalEvidence, _ := pool.PendingEvidence(-1)
	require.Empty(t, proposalEvidence)
	// Comet must still reject evidence already committed on the source chain.
	require.ErrorContains(t, pool.CheckEvidence(cmttypes.EvidenceList{committed}), "already committed")
	proposalEvidence, _ = pool.PendingEvidence(-1)
	require.Empty(t, proposalEvidence)
}

func seedTestnetEvidence(t *testing.T, f *testnetStateFixture) (*cmttypes.DuplicateVoteEvidence, map[string][]byte) {
	t.Helper()
	return seedTestnetEvidenceCount(t, f, 1)
}

func seedTestnetEvidenceCount(t *testing.T, f *testnetStateFixture, pendingCount int) (*cmttypes.DuplicateVoteEvidence, map[string][]byte) {
	t.Helper()
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.ctx.Config})
	require.NoError(t, err)
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: f.ctx.Config})
	require.NoError(t, err)
	evidenceDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "evidence", Config: f.ctx.Config})
	require.NoError(t, err)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
	state, err := stateStore.Load()
	require.NoError(t, err)
	height := state.LastBlockHeight
	pool, err := evidence.NewPool(evidenceDB, stateStore, store.NewBlockStore(blockDB))
	require.NoError(t, err)
	committed, err := cmttypes.NewMockDuplicateVoteEvidenceWithValidator(height, f.blockTimes[height], f.sourceSigner, state.ChainID)
	require.NoError(t, err)
	require.NoError(t, pool.AddEvidence(committed))
	state.LastBlockHeight++
	state.LastBlockTime = state.LastBlockTime.Add(time.Second)
	pool.Update(state, cmttypes.EvidenceList{committed})
	committedRecords := testnetDBContents(t, evidenceDB)
	require.Len(t, committedRecords, 1)
	// Seed through Comet's pool rather than copying its private key format.
	// This exercises the real v0.38.12 pending/committed prefix distinction.
	pendingEvidence := make(cmttypes.EvidenceList, 0, pendingCount)
	for i := 0; i < pendingCount; i++ {
		pending, err := cmttypes.NewMockDuplicateVoteEvidenceWithValidator(height, f.blockTimes[height], f.sourceSigner, state.ChainID)
		require.NoError(t, err)
		require.NoError(t, pool.AddEvidence(pending))
		pendingEvidence = append(pendingEvidence, pending)
	}
	proposalEvidence, _ := pool.PendingEvidence(-1)
	require.ElementsMatch(t, pendingEvidence, proposalEvidence)
	require.Len(t, testnetDBContents(t, evidenceDB), 1+pendingCount)
	require.NoError(t, pool.Close())
	require.NoError(t, stateDB.Close())
	require.NoError(t, blockDB.Close())
	return committed, committedRecords
}

func TestTestnetifyReplacesAddrbookAtConfiguredPath(t *testing.T) {
	for _, tc := range []struct {
		name          string
		path          string
		absolute      bool
		absent        bool
		missingParent bool
	}{
		{name: "default read only"},
		{name: "relative read only", path: filepath.Join("custom", "addrbook.json")},
		{name: "absolute read only", absolute: true},
		{name: "absent", path: filepath.Join("custom", "missing.json"), absent: true},
		{name: "missing parent", path: filepath.Join("custom", "nested", "addrbook.json"), absent: true, missingParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestnetStateFixture(t, 3, 3, 3, true)
			defaultPath := f.ctx.Config.P2P.AddrBookFile()
			original, err := os.ReadFile(defaultPath)
			require.NoError(t, err)
			if tc.absolute {
				f.ctx.Config.P2P.AddrBook = filepath.Join(t.TempDir(), "addrbook.json")
			} else if tc.path != "" {
				f.ctx.Config.P2P.AddrBook = tc.path
			}
			path := f.ctx.Config.P2P.AddrBookFile()
			if tc.missingParent {
				_, err := os.Stat(filepath.Dir(path))
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			}
			var oldFile *os.File
			if tc.absent {
				_, err := os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, os.WriteFile(path, original, 0o600))
				require.NoError(t, os.Chmod(path, 0o400))
				oldFile, err = os.Open(path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, oldFile.Close()) })
			}
			_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
			require.NoError(t, err)
			replacement, err := os.ReadFile(path)
			require.NoError(t, err)
			require.JSONEq(t, `{}`, string(replacement))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			if tc.missingParent {
				parentInfo, err := os.Stat(filepath.Dir(path))
				require.NoError(t, err)
				require.Zero(t, parentInfo.Mode().Perm()&0o077, "new address-book parent must remain private")
			}
			if oldFile != nil {
				// An open descriptor proves replacement even when elevated tests
				// could overwrite a read-only file in place.
				originalAfter, err := io.ReadAll(oldFile)
				require.NoError(t, err)
				require.Equal(t, original, originalAfter)
			}
			if path != defaultPath {
				defaultAfter, err := os.ReadFile(defaultPath)
				require.NoError(t, err)
				require.Equal(t, original, defaultAfter)
			}
		})
	}
}

func TestTestnetifyInvalidSigningStatePreservesPendingEvidence(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	seedTestnetEvidence(t, f)
	require.NoError(t, os.WriteFile(f.ctx.Config.PrivValidatorStateFile(), []byte(`{"height":"3","round":0,"step":0}`), 0o600))
	filesBefore := snapshotTestnetFiles(t, f)
	storesBefore := snapshotTestnetStores(t, f.ctx.Config, "evidence")
	_, err := testnetify(f.ctx, f.creator, f.appDB, nil)
	require.Error(t, err)
	require.False(t, f.creatorCalled)
	require.Equal(t, filesBefore, snapshotTestnetFiles(t, f))
	require.Equal(t, storesBefore, snapshotTestnetStores(t, f.ctx.Config, "evidence"))
}

func TestTestnetifyRejectedPreflightPreservesPendingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		appHeight      int64
		changeAddrbook bool
		addrbook       string
		absolute       bool
		creatorCalled  bool
	}{
		{name: "unsupported application height", appHeight: 4, creatorCalled: true},
		{name: "empty address book", appHeight: 3, changeAddrbook: true},
		{name: "dot address book", appHeight: 3, changeAddrbook: true, addrbook: "."},
		{name: "dot slash address book", appHeight: 3, changeAddrbook: true, addrbook: "./"},
		{name: "parent directory address book", appHeight: 3, changeAddrbook: true, addrbook: "config/.."},
		{name: "file as address book parent", appHeight: 3, changeAddrbook: true, addrbook: "config/genesis.json/addrbook.json"},
		{name: "absolute trailing separator", appHeight: 3, changeAddrbook: true, addrbook: "custom/nested/addrbook.json/", absolute: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Aligned Comet stores pass the initial height checks. The app-height
			// case fails only after creating the application and reading Info.
			f := newTestnetStateFixture(t, 3, 3, tc.appHeight, true)
			seedTestnetEvidence(t, f)
			filesBefore := snapshotTestnetFiles(t, f)
			storesBefore := snapshotTestnetStores(t, f.ctx.Config, "evidence")
			addrbook := f.ctx.Config.P2P.AddrBook
			if tc.changeAddrbook {
				f.ctx.Config.P2P.AddrBook = tc.addrbook
			}
			if tc.absolute {
				// Preserve the trailing separator; filepath.Join would clean it.
				f.ctx.Config.P2P.AddrBook = f.ctx.Config.RootDir + string(os.PathSeparator) + tc.addrbook
				_, err := os.Stat(filepath.Clean(f.ctx.Config.P2P.AddrBookFile()))
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			configuredAddrbook := f.ctx.Config.P2P.AddrBook
			_, err := testnetify(f.ctx, f.creator, f.appDB, nil)
			require.Error(t, err)
			require.Equal(t, tc.creatorCalled, f.creatorCalled)
			require.Equal(t, configuredAddrbook, f.ctx.Config.P2P.AddrBook, "preflight must not normalize the configured path")
			if tc.absolute {
				_, err := os.Stat(filepath.Clean(f.ctx.Config.P2P.AddrBookFile()))
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			f.ctx.Config.P2P.AddrBook = addrbook
			require.Equal(t, filesBefore, snapshotTestnetFiles(t, f))
			require.Equal(t, storesBefore, snapshotTestnetStores(t, f.ctx.Config, "evidence"))
		})
	}
}

func TestTestnetifyRejectedPreflightDoesNotCreatePaths(t *testing.T) {
	for _, tc := range []struct {
		name          string
		appHeight     int64
		addrbook      string
		absolute      bool
		missingParent bool
		creatorCalled bool
	}{
		{name: "unsupported application height", appHeight: 4, addrbook: "custom/nested/addrbook.json", missingParent: true, creatorCalled: true},
		{name: "absolute trailing separator", appHeight: 3, addrbook: "custom/nested/addrbook.json/", absolute: true, missingParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestnetStateFixture(t, 3, 3, tc.appHeight, true)
			addrbook := f.ctx.Config.P2P.AddrBook
			// Snapshot the source address book before configuring an invalid path.
			filesBefore := snapshotTestnetFiles(t, f)
			f.ctx.Config.P2P.AddrBook = tc.addrbook
			if tc.absolute {
				f.ctx.Config.P2P.AddrBook = f.ctx.Config.RootDir + string(os.PathSeparator) + tc.addrbook
			}
			absentPaths := []string{filepath.Join(f.ctx.Config.DBDir(), "evidence.db")}
			if tc.missingParent {
				absentPaths = append(absentPaths, filepath.Dir(f.ctx.Config.P2P.AddrBookFile()))
			}
			for _, path := range absentPaths {
				_, err := os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			// Opening a store to snapshot it would create the very path under test.
			storesBefore := snapshotTestnetStores(t, f.ctx.Config)
			_, err := testnetify(f.ctx, f.creator, f.appDB, nil)
			require.Error(t, err)
			require.Equal(t, tc.creatorCalled, f.creatorCalled)
			for _, path := range absentPaths {
				_, err := os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			f.ctx.Config.P2P.AddrBook = addrbook
			require.Equal(t, filesBefore, snapshotTestnetFiles(t, f))
			require.Equal(t, storesBefore, snapshotTestnetStores(t, f.ctx.Config))
		})
	}
}

func TestDeleteTestnetPendingEvidenceFlushesAndRetries(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	_, committedRecords := seedTestnetEvidence(t, f)
	db := openTestnetDB(t, f.ctx.Config, "evidence")
	before := testnetDBContents(t, db)
	tracking := &testnetSyncFailureDB{DB: db, failSyncWrite: 1}
	require.ErrorIs(t, deleteTestnetPendingEvidence(tracking), errTestnetSyncFailure)
	require.Equal(t, before, testnetDBContents(t, db))
	require.NoError(t, deleteTestnetPendingEvidence(tracking))
	require.Equal(t, 2, tracking.syncWrites)
	require.Zero(t, tracking.asyncWrites)
	require.Equal(t, committedRecords, testnetDBContents(t, db))
}

func TestDeleteTestnetPendingEvidenceAbortsOnIteratorErrors(t *testing.T) {
	for _, name := range []string{"iterator creation", "mid iteration", "iterator close"} {
		t.Run(name, func(t *testing.T) {
			f := newTestnetStateFixture(t, 3, 3, 3, true)
			seedTestnetEvidenceCount(t, f, 2)
			db := openTestnetDB(t, f.ctx.Config, "evidence")
			before := testnetDBContents(t, db)
			tracking := &testnetSyncFailureDB{DB: db}
			switch name {
			case "iterator creation":
				tracking.iteratorError = errTestnetSyncFailure
			case "mid iteration":
				tracking.iteratorIterationError = errTestnetSyncFailure
			case "iterator close":
				tracking.iteratorCloseError = errTestnetSyncFailure
			}
			require.ErrorIs(t, deleteTestnetPendingEvidence(tracking), errTestnetSyncFailure)
			if name == "mid iteration" {
				require.Equal(t, 1, tracking.iteratorKeysRead, "one of two pending records was read before iteration failed")
			}
			if name == "iterator creation" {
				require.Zero(t, tracking.iteratorCloseCalls)
			} else {
				require.Equal(t, 1, tracking.iteratorCloseCalls)
			}
			require.Zero(t, tracking.syncWrites)
			require.Zero(t, tracking.asyncWrites)
			require.Equal(t, before, testnetDBContents(t, db))
		})
	}
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
		seedTestnetEvidence(t, f)
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
		storesBefore := snapshotTestnetStores(t, f.ctx.Config, "evidence")
		_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
		require.Error(t, err)
		require.True(t, f.creatorCalled, "the app must report its committed height before halt recovery")
		require.Equal(t, filesBefore, snapshotTestnetFiles(t, f))
		require.Equal(t, storesBefore, snapshotTestnetStores(t, f.ctx.Config, "evidence"))
		return
	}
	// Missing records and empty bytes retain Comet's existing error behavior.
	// Malformed proto and missing response payload exercise the added guard:
	// Comet's unguarded loader exits or panics. Isolate each case in a child
	// and require normal test completion without deleting source evidence.
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

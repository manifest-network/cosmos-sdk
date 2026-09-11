package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	cmtdb "github.com/cometbft/cometbft-db"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
)

func TestTestnetifyReconstructsConsensusAtReconciledHeight(t *testing.T) {
	for _, extensions := range []bool{false, true} {
		for _, tc := range []struct {
			name                                string
			stateHeight, storeHeight, appHeight int64
		}{
			{"aligned", 3, 3, 3},
			{"rollback", 3, 4, 3},
			{"halt", 3, 4, 4},
		} {
			t.Run(fmt.Sprintf("%s/extensions=%v", tc.name, extensions), func(t *testing.T) {
				f := newTestnetStateFixture(t, tc.stateHeight, tc.storeHeight, tc.appHeight, extensions)
				application, err := testnetify(f.ctx, f.creator, f.appDB, nil)
				require.NoError(t, err)
				require.Same(t, f.application, application)
				require.True(t, f.creatorCalled)

				stateDB := openTestnetDB(t, f.ctx.Config, "state")
				stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
				state, err := stateStore.Load()
				require.NoError(t, err)
				require.Equal(t, tc.appHeight, state.LastBlockHeight)
				require.Equal(t, "fork-chain", state.ChainID)
				require.Equal(t, f.blockIDs[tc.appHeight], state.LastBlockID)
				require.Equal(t, testnetAppHash(tc.appHeight), state.AppHash)
				require.Equal(t, f.blockTimes[tc.appHeight], state.LastBlockTime)
				require.Equal(t, tc.appHeight, state.LastHeightValidatorsChanged)

				// These are the actual snapshots CometBFT loads when rebuilding
				// last-commit votes and proposing the first two fork blocks.
				for height := tc.appHeight; height <= tc.appHeight+2; height++ {
					validators, err := stateStore.LoadValidators(height)
					require.NoError(t, err, "validator history at %d", height)
					require.Len(t, validators.Validators, 1)
					require.Equal(t, f.forkKey, validators.Validators[0].Address)
					require.Equal(t, int64(900000000000000), validators.Validators[0].VotingPower)
				}
				blockStore := store.NewBlockStore(openTestnetDB(t, f.ctx.Config, "blockstore"))
				require.Equal(t, tc.appHeight, blockStore.Height())
				validators, err := stateStore.LoadValidators(tc.appHeight)
				require.NoError(t, err)
				seen := blockStore.LoadSeenCommit(tc.appHeight)
				require.NotNil(t, seen)
				require.Equal(t, state.LastBlockID, seen.BlockID)
				require.True(t, seen.ToVoteSet(state.ChainID, validators).HasTwoThirdsMajority())
				extended := blockStore.LoadBlockExtendedCommit(tc.appHeight)
				if extensions {
					require.NotNil(t, extended)
					require.NoError(t, extended.EnsureExtensions(true))
					require.True(t, extended.ToExtendedVoteSet(state.ChainID, validators).HasTwoThirdsMajority())
				} else {
					require.Nil(t, extended)
				}
				require.Nil(t, blockStore.LoadSeenCommit(tc.appHeight+1))
				require.Nil(t, blockStore.LoadBlockExtendedCommit(tc.appHeight+1))
				if tc.name == "halt" {
					require.Equal(t, sm.TxResultsHash(f.finalized.TxResults), state.LastResultsHash)
					require.Equal(t, f.finalized.ConsensusParamUpdates.Block.MaxBytes, state.ConsensusParams.Block.MaxBytes)
					require.Equal(t, f.finalized.ConsensusParamUpdates.Version.App, state.Version.Consensus.App)
				}
				genesis, err := genutiltypes.AppGenesisFromFile(f.ctx.Config.GenesisFile())
				require.NoError(t, err)
				require.Equal(t, state.ChainID, genesis.ChainID)
				_, cachedGenesis, err := node.LoadStateFromDBOrGenesisDocProvider(stateDB, node.DefaultGenesisDocProviderFunc(f.ctx.Config))
				require.NoError(t, err)
				require.Equal(t, state.ChainID, cachedGenesis.ChainID)
			})
		}
	}
}

func TestTestnetifyPreservesAppStateWithDifferentGenesisFormatting(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	appState := json.RawMessage(`{"bank":{"balances":[{"address":"manifest1snapshot","coins":[{"denom":"umfx","amount":"123456789"}]}]},"billing":{"params":{"epoch":42,"enabled":true},"memo":"preserve nested state and spaces inside strings"}}`)
	genesis, err := genutiltypes.AppGenesisFromFile(f.ctx.Config.GenesisFile())
	require.NoError(t, err)
	genesis.AppState = appState
	require.NoError(t, genesis.SaveAs(f.ctx.Config.GenesisFile()))
	fileBefore, err := genutiltypes.AppGenesisFromFile(f.ctx.Config.GenesisFile())
	require.NoError(t, err)
	cachedGenesis, err := genesis.ToGenesisDoc()
	require.NoError(t, err)
	cachedBytes, err := cmtjson.Marshal(cachedGenesis)
	require.NoError(t, err)
	var cachedBefore cmttypes.GenesisDoc
	require.NoError(t, cmtjson.Unmarshal(cachedBytes, &cachedBefore))
	// SaveAs indents RawMessage contents, while the cached document is compact.
	// Reusing the file's backing buffer to decode that shorter cache corrupts
	// the app_state that the conversion subsequently writes back to disk.
	require.Greater(t, len(fileBefore.AppState), len(cachedBefore.AppState))
	require.JSONEq(t, string(appState), string(fileBefore.AppState))
	require.JSONEq(t, string(appState), string(cachedBefore.AppState))
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: f.ctx.Config})
	require.NoError(t, err)
	require.NoError(t, stateDB.SetSync(testnetGenesisDocKey(), cachedBytes))
	require.NoError(t, stateDB.Close())

	_, err = testnetify(f.ctx, f.creator, f.appDB, nil)
	require.NoError(t, err)
	fileAfter, err := genutiltypes.AppGenesisFromFile(f.ctx.Config.GenesisFile())
	require.NoError(t, err)
	require.Equal(t, "fork-chain", fileAfter.ChainID)
	require.JSONEq(t, string(appState), string(fileAfter.AppState))
	_, cachedAfter, err := node.LoadStateFromDBOrGenesisDocProvider(openTestnetDB(t, f.ctx.Config, "state"), node.DefaultGenesisDocProviderFunc(f.ctx.Config))
	require.NoError(t, err)
	require.Equal(t, "fork-chain", cachedAfter.ChainID)
	require.JSONEq(t, string(appState), string(cachedAfter.AppState))
}

func TestTestnetifyRejectsUnsafePreflightWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		stateHeight, storeHeight int64
		change                   func(*testing.T, *testnetStateFixture)
	}{
		{name: "zero state height", stateHeight: 0, storeHeight: 1},
		{name: "zero block store height", stateHeight: 1, storeHeight: 0},
		{name: "store behind state", stateHeight: 3, storeHeight: 2},
		{name: "store two blocks ahead", stateHeight: 2, storeHeight: 4},
		{name: "missing key", stateHeight: 3, storeHeight: 3, change: func(t *testing.T, f *testnetStateFixture) {
			require.NoError(t, os.Remove(f.ctx.Config.PrivValidatorKeyFile()))
		}},
		{name: "malformed key", stateHeight: 3, storeHeight: 3, change: func(t *testing.T, f *testnetStateFixture) {
			require.NoError(t, os.WriteFile(f.ctx.Config.PrivValidatorKeyFile(), []byte("{"), 0o600))
		}},
		{name: "missing signing state", stateHeight: 3, storeHeight: 3, change: func(t *testing.T, f *testnetStateFixture) {
			require.NoError(t, os.Remove(f.ctx.Config.PrivValidatorStateFile()))
		}},
		{name: "malformed signing state", stateHeight: 3, storeHeight: 3, change: func(t *testing.T, f *testnetStateFixture) {
			require.NoError(t, os.WriteFile(f.ctx.Config.PrivValidatorStateFile(), []byte("{"), 0o600))
		}},
		{name: "signed height", stateHeight: 3, storeHeight: 3, change: changeTestnetSigningState(privval.FilePVLastSignState{Height: 3})},
		{name: "signed round", stateHeight: 3, storeHeight: 3, change: changeTestnetSigningState(privval.FilePVLastSignState{Round: 1})},
		{name: "signed step", stateHeight: 3, storeHeight: 3, change: changeTestnetSigningState(privval.FilePVLastSignState{Step: 2})},
		{name: "remaining signature", stateHeight: 3, storeHeight: 3, change: changeTestnetSigningState(privval.FilePVLastSignState{Signature: []byte{1}})},
		{name: "remaining sign bytes", stateHeight: 3, storeHeight: 3, change: changeTestnetSigningState(privval.FilePVLastSignState{SignBytes: []byte{1}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTestnetStateFixture(t, tc.stateHeight, tc.storeHeight, tc.stateHeight, true)
			if tc.change != nil {
				tc.change(t, f)
			}
			files := snapshotTestnetFiles(t, f)
			before := snapshotTestnetStores(t, f.ctx.Config)
			_, err := testnetify(f.ctx, f.creator, f.appDB, nil)
			require.Error(t, err)
			require.False(t, f.creatorCalled, "unsafe preflight must not invoke the app rewrite")
			require.Equal(t, files, snapshotTestnetFiles(t, f))
			require.Equal(t, before, snapshotTestnetStores(t, f.ctx.Config))
		})
	}
}

func TestLoadTestnetPrivValidatorDoesNotWriteFiles(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 3, 3, true)
	before := snapshotTestnetFiles(t, f)
	pv, err := loadTestnetPrivValidator(f.ctx.Config.PrivValidatorKeyFile(), f.ctx.Config.PrivValidatorStateFile())
	require.NoError(t, err)
	key, err := pv.GetPubKey()
	require.NoError(t, err)
	require.Equal(t, f.forkKey, key.Address())
	require.Equal(t, before, snapshotTestnetFiles(t, f))
}

func TestReconcileTestnetHeightRejectsUncommittedApplication(t *testing.T) {
	for _, heights := range [][3]int64{
		{0, 1, 1}, {2, 3, 3}, {2, 3, 4}, {5, 3, 4}, {4, 3, 3},
	} {
		t.Run(fmt.Sprint(heights), func(t *testing.T) {
			_, _, err := reconcileTestnetHeight(heights[0], heights[1], heights[2])
			require.Error(t, err)
		})
	}
}

func TestRollbackTestnetBlockRetriesAfterCommitCleanupFailure(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 4, 3, true)
	db := openTestnetDB(t, f.ctx.Config, "blockstore")
	blockStore := store.NewBlockStore(db)
	target := blockStore.Height()
	before := testnetDBContents(t, db)
	failing := &testnetSyncFailureDB{DB: db, failKey: testnetExtendedCommitKey(target), fail: true}
	require.ErrorContains(t, rollbackTestnetBlock(blockStore, failing), errTestnetSyncFailure.Error())
	require.Equal(t, target, blockStore.Height(), "cleanup failure must not lower the persisted height")
	require.Equal(t, before, testnetDBContents(t, db))
	require.NoError(t, rollbackTestnetBlock(blockStore, failing))
	reopened := store.NewBlockStore(db)
	require.Equal(t, target-1, reopened.Height())
	require.NotNil(t, reopened.LoadBlock(target-1))
	require.Nil(t, reopened.LoadBlock(target))
	require.Nil(t, reopened.LoadSeenCommit(target))
	require.Nil(t, reopened.LoadBlockExtendedCommit(target))
}

func TestRollbackTestnetBlockRetriesAfterBlockDeletionFailure(t *testing.T) {
	f := newTestnetStateFixture(t, 3, 4, 3, true)
	db := openTestnetDB(t, f.ctx.Config, "blockstore")
	// Fail CometBFT's batch after the extended commit deletion has reached disk.
	failing := &testnetSyncFailureDB{DB: db, failSyncWrite: 2}
	blockStore := store.NewBlockStore(failing)
	target := blockStore.Height()
	require.ErrorContains(t, rollbackTestnetBlock(blockStore, failing), errTestnetSyncFailure.Error())
	// CometBFT updates its in-memory height before the failed write. Reopen
	// from durable state, as a retry after restarting the command would do.
	reopened := store.NewBlockStore(failing)
	require.Equal(t, target, reopened.Height())
	require.NotNil(t, reopened.LoadBlock(target))
	require.NotNil(t, reopened.LoadSeenCommit(target))
	require.Nil(t, reopened.LoadBlockExtendedCommit(target))
	require.NoError(t, rollbackTestnetBlock(reopened, failing))
	reopened = store.NewBlockStore(db)
	require.Equal(t, target-1, reopened.Height())
	require.Nil(t, reopened.LoadBlock(target))
	require.Nil(t, reopened.LoadSeenCommit(target))
	require.Nil(t, reopened.LoadBlockExtendedCommit(target))
}

func TestDeleteTestnetCommitsFlushesBothRecordsAndPreservesNeighbors(t *testing.T) {
	db := cmtdb.NewMemDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	vote, _ := signedTestnetVote(t)
	for _, height := range []int64{vote.Height, vote.Height + 1, vote.Height + 2} {
		for _, key := range [][]byte{testnetSeenCommitKey(height), testnetExtendedCommitKey(height)} {
			require.NoError(t, db.Set(key, []byte("existing commit")))
		}
	}
	tracking := &testnetSyncFailureDB{DB: db}
	require.NoError(t, deleteTestnetCommits(tracking, vote.Height+1))
	require.Positive(t, tracking.syncWrites)
	require.Zero(t, tracking.asyncWrites)
	for _, height := range []int64{vote.Height, vote.Height + 1, vote.Height + 2} {
		for _, key := range [][]byte{testnetSeenCommitKey(height), testnetExtendedCommitKey(height)} {
			value, err := db.Get(key)
			require.NoError(t, err)
			if height == vote.Height+1 {
				require.Nil(t, value)
			} else {
				require.Equal(t, []byte("existing commit"), value)
			}
		}
	}
}

func TestRemoveTestnetWALPreservesUnrelatedFiles(t *testing.T) {
	directory := t.TempDir()
	wal := filepath.Join(directory, "consensus.wal")
	removed := []string{"consensus.wal", "consensus.wal.0", "consensus.wal.001", "consensus.wal.42"}
	preserved := []string{"consensus.wal.backup", "consensus.wal.2.backup", "consensus.wal.", "other.wal", "README"}
	for _, name := range append(removed, preserved...) {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600))
	}
	require.NoError(t, removeTestnetWAL(wal))
	for _, name := range removed {
		_, err := os.Stat(filepath.Join(directory, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	for _, name := range preserved {
		data, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Equal(t, []byte(name), data)
	}
	require.NoError(t, removeTestnetWAL(wal))
	require.NoError(t, removeTestnetWAL(filepath.Join(directory, "missing", "wal")))
}

type testnetStateFixture struct {
	ctx           *Context
	appDB         dbm.DB
	application   *testnetInfoApplication
	creatorCalled bool
	forkKey       cmttypes.Address
	sourceSigner  cmttypes.PrivValidator
	blockIDs      map[int64]cmttypes.BlockID
	blockTimes    map[int64]time.Time
	finalized     *abci.ResponseFinalizeBlock
}

func (f *testnetStateFixture) creator(_ log.Logger, _ dbm.DB, _ io.Writer, _ servertypes.AppOptions) servertypes.Application {
	f.creatorCalled = true
	return f.application
}

type testnetInfoApplication struct {
	servertypes.Application
	height int64
}

func (app *testnetInfoApplication) Info(*abci.RequestInfo) (*abci.ResponseInfo, error) {
	return &abci.ResponseInfo{LastBlockHeight: app.height, LastBlockAppHash: testnetAppHash(app.height)}, nil
}

func newTestnetStateFixture(t *testing.T, stateHeight, storeHeight, appHeight int64, extensions bool) *testnetStateFixture {
	t.Helper()
	ctx := NewDefaultContext()
	ctx.Logger = log.NewNopLogger()
	ctx.Config.SetRoot(t.TempDir())
	ctx.Config.DBBackend = string(cmtdb.GoLevelDBBackend)
	ctx.Viper.Set(KeyNewChainID, "fork-chain")
	for _, directory := range []string{filepath.Dir(ctx.Config.GenesisFile()), ctx.Config.DBDir()} {
		require.NoError(t, os.MkdirAll(directory, 0o700))
	}
	pv := privval.GenFilePV(ctx.Config.PrivValidatorKeyFile(), ctx.Config.PrivValidatorStateFile())
	pv.Save()
	forkKey, err := pv.GetPubKey()
	require.NoError(t, err)
	sourcePV := cmttypes.NewMockPV()
	sourceKey, err := sourcePV.GetPubKey()
	require.NoError(t, err)
	genesis := genutiltypes.NewAppGenesisWithVersion("source-chain", json.RawMessage(`{}`))
	genesis.Consensus.Validators = []cmttypes.GenesisValidator{{PubKey: sourceKey, Power: 10}}
	require.NoError(t, genesis.ValidateAndComplete())
	if extensions {
		genesis.Consensus.Params.ABCI.VoteExtensionsEnableHeight = 1
	}
	require.NoError(t, genesis.SaveAs(ctx.Config.GenesisFile()))
	require.NoError(t, os.WriteFile(filepath.Join(ctx.Config.RootDir, "config", "addrbook.json"), []byte(`{"source-peer":"keep until successful conversion"}`), 0o600))
	genDoc, err := genesis.ToGenesisDoc()
	require.NoError(t, err)
	state, err := sm.MakeGenesisState(genDoc)
	require.NoError(t, err)
	f := &testnetStateFixture{
		ctx: ctx, appDB: dbm.NewMemDB(), application: &testnetInfoApplication{height: appHeight},
		forkKey: forkKey.Address(), sourceSigner: sourcePV, blockIDs: make(map[int64]cmttypes.BlockID), blockTimes: make(map[int64]time.Time),
	}
	t.Cleanup(func() { require.NoError(t, f.appDB.Close()) })
	blockDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "blockstore", Config: ctx.Config})
	require.NoError(t, err)
	blockStore := store.NewBlockStore(blockDB)
	lastCommit := &cmttypes.Commit{}
	for height := int64(1); height <= storeHeight; height++ {
		block := cmttypes.MakeBlock(height, []cmttypes.Tx{[]byte("source transaction")}, lastCommit, nil)
		block.ChainID = genesis.ChainID
		block.Time = genesis.GenesisTime.Add(time.Duration(height) * time.Second)
		block.ValidatorsHash = state.Validators.Hash()
		block.NextValidatorsHash = state.NextValidators.Hash()
		block.ConsensusHash = state.ConsensusParams.Hash()
		block.ProposerAddress = sourceKey.Address()
		block.AppHash = testnetAppHash(height - 1)
		block.LastBlockID = f.blockIDs[height-1]
		parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		id := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		f.blockIDs[height], f.blockTimes[height] = id, block.Time
		vote := cmttypes.Vote{Type: cmtproto.PrecommitType, Height: height, Round: 0, BlockID: id, Timestamp: block.Time, ValidatorAddress: sourceKey.Address()}
		protoVote := vote.ToProto()
		require.NoError(t, sourcePV.SignVote(genesis.ChainID, protoVote))
		signed, err := cmttypes.VoteFromProto(protoVote)
		require.NoError(t, err)
		commit := &cmttypes.ExtendedCommit{Height: height, BlockID: id, ExtendedSignatures: []cmttypes.ExtendedCommitSig{signed.ExtendedCommitSig()}}
		blockStore.SaveBlockWithExtendedCommit(block, parts, commit)
		lastCommit = commit.ToCommit()
	}
	// A stored vote for a not-yet-retained block must never leak into the fork.
	stale, _ := signedTestnetVote(t)
	stale.Height = storeHeight + 1
	require.NoError(t, saveTestnetCommit(blockDB, stale, true))
	require.NoError(t, blockDB.Close())
	state.LastBlockHeight = stateHeight
	state.LastBlockID = f.blockIDs[stateHeight]
	if stateHeight > 0 {
		state.LastBlockTime = f.blockTimes[stateHeight]
		state.LastValidators = state.Validators.Copy()
	}
	state.AppHash = testnetAppHash(stateHeight)
	stateDB, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: "state", Config: ctx.Config})
	require.NoError(t, err)
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{})
	require.NoError(t, stateStore.Bootstrap(state))
	genesisBytes, err := cmtjson.Marshal(genDoc)
	require.NoError(t, err)
	require.NoError(t, stateDB.SetSync(testnetGenesisDocKey(), genesisBytes))
	f.finalized = &abci.ResponseFinalizeBlock{
		AppHash:   testnetAppHash(storeHeight),
		TxResults: []*abci.ExecTxResult{{Code: 0, GasWanted: 10, GasUsed: 8}},
		ConsensusParamUpdates: &cmtproto.ConsensusParams{
			Block:   &cmtproto.BlockParams{MaxBytes: 12345678, MaxGas: 789},
			Version: &cmtproto.VersionParams{App: 7},
		},
	}
	if storeHeight > 0 {
		require.NoError(t, stateStore.SaveFinalizeBlockResponse(storeHeight, f.finalized))
	}
	require.NoError(t, stateDB.Close())
	return f
}

func testnetAppHash(height int64) []byte { return []byte(fmt.Sprintf("app hash at %d", height)) }

func openTestnetDB(t *testing.T, config *cmtcfg.Config, name string) cmtdb.DB {
	t.Helper()
	db, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: name, Config: config})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func changeTestnetSigningState(state privval.FilePVLastSignState) func(*testing.T, *testnetStateFixture) {
	return func(t *testing.T, f *testnetStateFixture) {
		data, err := cmtjson.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(f.ctx.Config.PrivValidatorStateFile(), data, 0o600))
	}
}

type testnetFileSnapshot struct {
	Data    []byte
	Mode    os.FileMode
	ModTime time.Time
}

func snapshotTestnetFiles(t *testing.T, f *testnetStateFixture) map[string]testnetFileSnapshot {
	t.Helper()
	files := make(map[string]testnetFileSnapshot)
	for _, path := range []string{f.ctx.Config.GenesisFile(), f.ctx.Config.PrivValidatorKeyFile(), f.ctx.Config.PrivValidatorStateFile(), f.ctx.Config.P2P.AddrBookFile()} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		files[path] = testnetFileSnapshot{Data: data, Mode: info.Mode(), ModTime: info.ModTime()}
	}
	return files
}

func snapshotTestnetStores(t *testing.T, config *cmtcfg.Config, additionalStores ...string) map[string]map[string][]byte {
	t.Helper()
	contents := make(map[string]map[string][]byte)
	for _, name := range append([]string{"state", "blockstore"}, additionalStores...) {
		db, err := cmtcfg.DefaultDBProvider(&cmtcfg.DBContext{ID: name, Config: config})
		require.NoError(t, err)
		contents[name] = testnetDBContents(t, db)
		require.NoError(t, db.Close())
	}
	return contents
}

func testnetDBContents(t *testing.T, db cmtdb.DB) map[string][]byte {
	t.Helper()
	iterator, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	contents := make(map[string][]byte)
	for ; iterator.Valid(); iterator.Next() {
		contents[string(iterator.Key())] = bytes.Clone(iterator.Value())
	}
	require.NoError(t, iterator.Error())
	require.NoError(t, iterator.Close())
	return contents
}

var errTestnetSyncFailure = errors.New("injected commit cleanup failure")

type testnetSyncFailureDB struct {
	cmtdb.DB
	failKey                 []byte
	fail                    bool
	failSyncWrite           int
	iteratorError           error
	iteratorCloseError      error
	syncWrites, asyncWrites int
}

func (db *testnetSyncFailureDB) DeleteSync(key []byte) error {
	db.syncWrites++
	if db.syncWrites == db.failSyncWrite {
		return errTestnetSyncFailure
	}
	if db.fail && bytes.Equal(key, db.failKey) {
		db.fail = false
		return errTestnetSyncFailure
	}
	return db.DB.DeleteSync(key)
}

func (db *testnetSyncFailureDB) NewBatch() cmtdb.Batch {
	return &testnetSyncFailureBatch{Batch: db.DB.NewBatch(), db: db}
}

func (db *testnetSyncFailureDB) Iterator(start, end []byte) (cmtdb.Iterator, error) {
	if db.iteratorError != nil {
		return nil, db.iteratorError
	}
	iterator, err := db.DB.Iterator(start, end)
	if err != nil || db.iteratorCloseError == nil {
		return iterator, err
	}
	return &testnetCloseFailureIterator{Iterator: iterator, err: db.iteratorCloseError}, nil
}

type testnetCloseFailureIterator struct {
	cmtdb.Iterator
	err error
}

func (iterator *testnetCloseFailureIterator) Close() error {
	if err := iterator.Iterator.Close(); err != nil {
		return err
	}
	return iterator.err
}

type testnetSyncFailureBatch struct {
	cmtdb.Batch
	db            *testnetSyncFailureDB
	deletesTarget bool
}

func (batch *testnetSyncFailureBatch) Delete(key []byte) error {
	batch.deletesTarget = batch.deletesTarget || bytes.Equal(key, batch.db.failKey)
	return batch.Batch.Delete(key)
}

func (batch *testnetSyncFailureBatch) WriteSync() error {
	batch.db.syncWrites++
	if batch.db.syncWrites == batch.db.failSyncWrite {
		return errTestnetSyncFailure
	}
	if batch.db.fail && batch.deletesTarget {
		batch.db.fail = false
		return errTestnetSyncFailure
	}
	return batch.Batch.WriteSync()
}

func (batch *testnetSyncFailureBatch) Write() error {
	batch.db.asyncWrites++
	return batch.Batch.Write()
}

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	cmtdb "github.com/cometbft/cometbft-db"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	pvm "github.com/cometbft/cometbft/privval"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
)

// CometBFT v0.38 does not expose keys for replacing an existing extended commit.
// Keep this compatibility boundary shared by replacement, cleanup and tests.
func testnetSeenCommitKey(height int64) []byte {
	return []byte(fmt.Sprintf("SC:%v", height))
}

func testnetExtendedCommitKey(height int64) []byte {
	return []byte(fmt.Sprintf("EC:%v", height))
}

func validateTestnetStoreHeights(stateHeight, storeHeight int64) error {
	if stateHeight < 1 || storeHeight < 1 {
		return fmt.Errorf("in-place-testnet requires committed state and local blocks (state height %d, blockstore height %d)", stateHeight, storeHeight)
	}
	// Conversion writes history at the reconciled height plus two.
	if storeHeight > math.MaxInt64-2 {
		return fmt.Errorf("blockstore height %d is too large to convert", storeHeight)
	}
	if storeHeight < stateHeight || storeHeight-stateHeight > 1 {
		return fmt.Errorf("unsupported in-place-testnet heights: state %d, blockstore %d; blockstore must equal state or be one block ahead", stateHeight, storeHeight)
	}
	return nil
}

func reconcileTestnetHeight(appHeight, stateHeight, storeHeight int64) (height int64, rollback bool, err error) {
	if err := validateTestnetStoreHeights(stateHeight, storeHeight); err != nil {
		return 0, false, err
	}
	switch {
	case appHeight == stateHeight:
		return stateHeight, storeHeight > stateHeight, nil
	case appHeight == storeHeight && storeHeight == stateHeight+1:
		return storeHeight, false, nil
	default:
		return 0, false, fmt.Errorf("unsupported in-place-testnet heights: application %d, state %d, blockstore %d; recover the source node before copying it", appHeight, stateHeight, storeHeight)
	}
}

// reconcileTestnetState checks the source snapshot without writing it. When the
// application committed the last block but CometBFT did not save state, recover
// the metadata that CometBFT's updateState derives from that block and its saved
// FinalizeBlock response. The caller replaces all three validator sets afterwards.
func reconcileTestnetState(state sm.State, stateStore sm.Store, blockStore *store.BlockStore, appHeight int64, appHash []byte) (sm.State, bool, error) {
	height, rollback, err := reconcileTestnetHeight(appHeight, state.LastBlockHeight, blockStore.Height())
	if err != nil {
		return state, false, err
	}
	block := blockStore.LoadBlock(height)
	meta := blockStore.LoadBlockMeta(height)
	if block == nil || meta == nil || !meta.BlockID.IsComplete() {
		return state, false, fmt.Errorf("in-place-testnet requires the full local block and metadata at height %d", height)
	}
	if block.Height != height || block.ChainID != state.ChainID || !bytes.Equal(meta.BlockID.Hash, block.Hash()) {
		return state, false, fmt.Errorf("block at height %d does not match the source state or metadata", height)
	}
	if height == state.LastBlockHeight {
		if !state.LastBlockID.Equals(meta.BlockID) || !bytes.Equal(appHash, state.AppHash) {
			return state, false, fmt.Errorf("application hash or last block ID does not match source state at height %d", height)
		}
		return state, rollback, nil
	}
	if !block.LastBlockID.Equals(state.LastBlockID) || !bytes.Equal(block.AppHash, state.AppHash) {
		return state, false, fmt.Errorf("committed block at height %d does not extend the source state", height)
	}
	response, err := stateStore.LoadLastFinalizeBlockResponse(height)
	if err != nil {
		return state, false, fmt.Errorf("load committed block response at height %d: %w", height, err)
	}
	if response == nil || len(response.TxResults) != len(block.Txs) {
		return state, false, fmt.Errorf("invalid committed block response at height %d", height)
	}
	for _, result := range response.TxResults {
		if result == nil {
			return state, false, fmt.Errorf("missing transaction result at height %d", height)
		}
	}
	// CometBFT also accepts an empty response hash immediately after a v0.37
	// upgrade and falls back to the hash reported by application Info.
	if len(response.AppHash) > 0 && !bytes.Equal(response.AppHash, appHash) {
		return state, false, fmt.Errorf("committed response hash does not match application at height %d", height)
	}
	if response.ConsensusParamUpdates != nil {
		params := state.ConsensusParams.Update(response.ConsensusParamUpdates)
		if err := params.ValidateBasic(); err != nil {
			return state, false, fmt.Errorf("validate committed consensus params: %w", err)
		}
		if err := state.ConsensusParams.ValidateUpdate(response.ConsensusParamUpdates, height); err != nil {
			return state, false, fmt.Errorf("validate committed consensus parameter update: %w", err)
		}
		state.ConsensusParams = params
		state.Version.Consensus.App = params.Version.App
		state.LastHeightConsensusParamsChanged = height + 1
	}
	state.LastBlockHeight = height
	state.LastBlockID = meta.BlockID
	state.LastBlockTime = block.Time
	state.LastResultsHash = sm.TxResultsHash(response.TxResults)
	state.AppHash = bytes.Clone(appHash)
	return state, false, nil
}

func deleteTestnetCommits(db cmtdb.DB, height int64) error {
	batch := db.NewBatch()
	defer batch.Close()
	if err := batch.Delete(testnetSeenCommitKey(height)); err != nil {
		return err
	}
	if err := batch.Delete(testnetExtendedCommitKey(height)); err != nil {
		return err
	}
	return batch.WriteSync()
}

// Delete the extended commit durably before CometBFT lowers the blockstore
// height. A failed block rollback can then be retried without leaving an orphan
// extended commit above a successfully lowered height. These are separate writes.
func rollbackTestnetBlock(blockStore *store.BlockStore, db cmtdb.DB) error {
	if err := db.DeleteSync(testnetExtendedCommitKey(blockStore.Height())); err != nil {
		return err
	}
	return blockStore.DeleteLatestBlock()
}

// loadTestnetPrivValidator requires an explicitly prepared signing identity.
// It never generates a key, resets signing state, or writes either input file.
func loadTestnetPrivValidator(keyPath, statePath string) (validator *pvm.FilePV, err error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read testnet private validator key: %w", err)
	}
	var key pvm.FilePVKey
	if err := cmtjson.Unmarshal(keyBytes, &key); err != nil {
		return nil, fmt.Errorf("decode testnet private validator key: %w", err)
	}
	if key.PrivKey == nil || key.PubKey == nil {
		return nil, fmt.Errorf("testnet private validator key must contain private and public keys")
	}
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("read testnet signing state (keep a reset JSON file with height, round and step zero): %w", err)
	}
	var signingState pvm.FilePVLastSignState
	if err := cmtjson.Unmarshal(stateBytes, &signingState); err != nil {
		return nil, fmt.Errorf("decode testnet signing state: %w", err)
	}
	var stateFields map[string]json.RawMessage
	if err := json.Unmarshal(stateBytes, &stateFields); err != nil {
		return nil, fmt.Errorf("decode testnet signing state fields: %w", err)
	}
	for _, field := range []string{"height", "round", "step"} {
		value, ok := stateFields[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("testnet signing state must contain %s", field)
		}
	}
	if signingState.Height != 0 || signingState.Round != 0 || signingState.Step != 0 || len(signingState.Signature) > 0 || len(signingState.SignBytes) > 0 {
		return nil, fmt.Errorf("in-place-testnet requires a fresh validator key and reset signing state with height, round and step zero and no signature")
	}
	// CometBFT crypto implementations can panic for malformed key lengths or
	// uninitialized keys. Convert those failures to a preflight error before any
	// signing-state or genesis writes, without including private key material.
	defer func() {
		if recover() != nil {
			validator = nil
			err = fmt.Errorf("invalid testnet private validator key")
		}
	}()
	validator = pvm.NewFilePV(key.PrivKey, keyPath, statePath)
	if !validator.Key.PubKey.Equals(key.PubKey) || !bytes.Equal(validator.Key.Address, key.Address) {
		return nil, fmt.Errorf("testnet validator public key or address does not match its private key")
	}
	message := []byte("in-place-testnet private key validation")
	signature, err := key.PrivKey.Sign(message)
	if err != nil || !key.PubKey.VerifySignature(message, signature) {
		return nil, fmt.Errorf("invalid testnet private validator key")
	}
	return validator, nil
}

// Remove only the configured WAL and its numeric rotation files, which CometBFT
// can also replay. A custom WAL directory may contain unrelated files.
func removeTestnetWAL(walPath string) error {
	entries, err := os.ReadDir(filepath.Dir(walPath))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read consensus WAL directory: %w", err)
	}
	base := filepath.Base(walPath)
	for _, entry := range entries {
		name := entry.Name()
		if name != base {
			suffix, found := strings.CutPrefix(name, base+".")
			if !found || suffix == "" || strings.IndexFunc(suffix, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
				continue
			}
		}
		if err := os.Remove(filepath.Join(filepath.Dir(walPath), name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove source consensus WAL: %w", err)
		}
	}
	return nil
}

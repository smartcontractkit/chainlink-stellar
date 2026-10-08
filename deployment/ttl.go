package deployment

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ContractCodeLedgerKey is the ledger key of an uploaded WASM blob.
func ContractCodeLedgerKey(wasmHash xdr.Hash) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: wasmHash},
	}
}

// ContractInstanceLedgerKey is the ledger key of a contract's instance entry.
func ContractInstanceLedgerKey(contractID string) (xdr.LedgerKey, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return xdr.LedgerKey{}, fmt.Errorf("invalid contract id %q: %w", contractID, err)
	}
	var id xdr.ContractId
	copy(id[:], raw)
	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id},
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}, nil
}

// ContractWasmHash returns the WASM hash a deployed contract instance currently runs.
func (d *Deployer) ContractWasmHash(ctx context.Context, contractID string) (xdr.Hash, error) {
	key, err := ContractInstanceLedgerKey(contractID)
	if err != nil {
		return xdr.Hash{}, err
	}
	data, err := d.fetchLedgerEntry(ctx, key)
	if err != nil {
		return xdr.Hash{}, fmt.Errorf("fetch instance of %s: %w", contractID, err)
	}
	cd, ok := data.GetContractData()
	if !ok || cd.Val.Instance == nil || cd.Val.Instance.Executable.WasmHash == nil {
		return xdr.Hash{}, fmt.Errorf("contract %s is not a WASM contract instance", contractID)
	}
	return *cd.Val.Instance.Executable.WasmHash, nil
}

// ContractInstanceState reads a contract's instance ledger entry, reporting
// whether the contract exists and, when it does, the WASM hash its instance
// currently runs. A contract that was never deployed reports exists=false with
// no error.
func (d *Deployer) ContractInstanceState(ctx context.Context, contractID string) (bool, xdr.Hash, error) {
	wasmHash, err := d.ContractWasmHash(ctx, contractID)
	if errors.Is(err, ErrLedgerEntryNotFound) {
		return false, xdr.Hash{}, nil
	}
	if err != nil {
		return false, xdr.Hash{}, err
	}
	return true, wasmHash, nil
}

// ExtendTTLToMax extends the given persistent ledger entries to the network's
// maximum TTL in one ExtendFootprintTtl transaction paid by the deployer, and
// returns each entry's live-until ledger afterwards, in key order. Extending is
// permissionless: no contract auth is involved.
//
// Extending a contract instance key does not extend its code; pass
// ContractCodeLedgerKey of the instance's WASM hash as well.
//
// Archived entries are restored first when auto-restore is enabled.
//
// The fee of a maximum extension can exceed the uint32 transaction-fee cap for
// large code entries; ExtendTTLTo reaches a chosen target instead.
func (d *Deployer) ExtendTTLToMax(ctx context.Context, keys []xdr.LedgerKey) ([]uint32, error) {
	if len(keys) == 0 {
		return nil, errors.New("no ledger keys to extend")
	}
	maxTTL, err := d.maxEntryTTL(ctx)
	if err != nil {
		return nil, err
	}
	// ExtendTo is relative to the current ledger and must stay below MaxEntryTtl.
	return d.extendTTL(ctx, keys, maxTTL-1)
}

// ExtendTTLTo extends the given persistent ledger entries until their
// remaining TTL reaches targetTTL ledgers, in one ExtendFootprintTtl
// transaction paid by the deployer, and returns each entry's live-until ledger
// afterwards, in key order. When every entry already has at least targetTTL
// ledgers of life left, no transaction is sent. Extending is permissionless:
// no contract auth is involved.
//
// targetTTL must stay below the network's MaxEntryTtl; use ExtendTTLToMax to
// reach the maximum. A target that keeps the simulated fee (plus the fee
// buffer) under the uint32 transaction-fee cap fits in a single transaction.
//
// Archived entries are restored first when auto-restore is enabled.
func (d *Deployer) ExtendTTLTo(ctx context.Context, keys []xdr.LedgerKey, targetTTL uint32) ([]uint32, error) {
	if len(keys) == 0 {
		return nil, errors.New("no ledger keys to extend")
	}
	maxTTL, err := d.maxEntryTTL(ctx)
	if err != nil {
		return nil, err
	}
	if targetTTL >= maxTTL {
		return nil, fmt.Errorf("target TTL %d must stay below the network maximum %d; use ExtendTTLToMax to reach it", targetTTL, maxTTL)
	}
	atTarget, err := d.entriesAtTTL(ctx, keys, targetTTL)
	if err != nil {
		return nil, err
	}
	if atTarget {
		return d.liveUntilLedgers(ctx, keys)
	}
	return d.extendTTL(ctx, keys, targetTTL)
}

// SimulateExtendTTL previews ExtendTTLTo without submitting anything. It
// returns the simulated minimum resource fee, or atTarget=true when every
// entry already has at least targetTTL ledgers of life left (fee 0).
func (d *Deployer) SimulateExtendTTL(ctx context.Context, keys []xdr.LedgerKey, targetTTL uint32) (fee int64, atTarget bool, err error) {
	if len(keys) == 0 {
		return 0, false, errors.New("no ledger keys to extend")
	}
	maxTTL, err := d.maxEntryTTL(ctx)
	if err != nil {
		return 0, false, err
	}
	if targetTTL >= maxTTL {
		return 0, false, fmt.Errorf("target TTL %d must stay below the network maximum %d", targetTTL, maxTTL)
	}
	atTarget, err = d.entriesAtTTL(ctx, keys, targetTTL)
	if err != nil {
		return 0, false, err
	}
	if atTarget {
		return 0, true, nil
	}
	_, sim, err := d.simulateExtend(ctx, keys, targetTTL, time.Now().Add(d.txnTimeBound))
	if err != nil {
		return 0, false, err
	}
	if sim.RestorePreamble != nil {
		return 0, false, errors.New("ledger entries are archived; restore them before extending")
	}
	return sim.MinResourceFee, false, nil
}

// extendTTL submits one ExtendFootprintTtl transaction that extends keys to
// extendTo ledgers from the current ledger, restoring archived entries first
// when auto-restore is enabled, and returns each key's live-until ledger.
func (d *Deployer) extendTTL(ctx context.Context, keys []xdr.LedgerKey, extendTo uint32) ([]uint32, error) {
	deadline := time.Now().Add(d.txnTimeBound)
	seq, sim, err := d.simulateExtend(ctx, keys, extendTo, deadline)
	if err != nil {
		return nil, err
	}
	if sim.RestorePreamble != nil {
		if !d.autoRestore {
			return nil, errors.New("ledger entries are archived and auto-restore is disabled; restore them before extending")
		}
		if err := d.restoreFootprint(ctx, *sim.RestorePreamble); err != nil {
			return nil, fmt.Errorf("failed to restore archived ledger entries: %w", err)
		}
		deadline = time.Now().Add(d.txnTimeBound)
		if seq, sim, err = d.simulateExtend(ctx, keys, extendTo, deadline); err != nil {
			return nil, err
		}
		if sim.RestorePreamble != nil {
			return nil, errors.New("simulation after restore still requires another restore: unexpected second RestorePreamble")
		}
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &sorobanData); err != nil {
		return nil, fmt.Errorf("failed to decode extend soroban data: %w", err)
	}
	sorobanData.ResourceFee += xdr.Int64(d.resourceFeeBump(sim.MinResourceFee))
	// A regular Soroban transaction cannot declare a fee above uint32; only
	// fee-bump transactions get the int64 range. Reject the extension here
	// with the remedy instead of failing opaquely at submit time.
	if int64(sorobanData.ResourceFee)+int64(txnbuild.MinBaseFee) > math.MaxUint32 {
		return nil, fmt.Errorf("extend fee %d stroops exceeds the uint32 transaction fee cap; lower the target TTL or extend fewer entries per transaction", int64(sorobanData.ResourceFee)+int64(txnbuild.MinBaseFee))
	}
	tx, err := d.buildExtendTransaction(seq, extendTo, sorobanData, deadline)
	if err != nil {
		return nil, err
	}

	if _, err := d.signSubmitAndWait(ctx, tx, deadline, "extend "); err != nil {
		return nil, fmt.Errorf("extend transaction failed: %w", err)
	}
	return d.liveUntilLedgers(ctx, keys)
}

// entriesAtTTL reports whether every key's entry has at least ttl ledgers of
// life left, relative to the latest closed ledger.
func (d *Deployer) entriesAtTTL(ctx context.Context, keys []xdr.LedgerKey, ttl uint32) (bool, error) {
	latest, err := d.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get latest ledger: %w", err)
	}
	liveUntil, err := d.liveUntilLedgers(ctx, keys)
	if err != nil {
		return false, err
	}
	for _, lu := range liveUntil {
		if int64(lu)-int64(latest.Sequence) < int64(ttl) {
			return false, nil
		}
	}
	return true, nil
}

// simulateExtend simulates a draft ExtendFootprintTtl transaction with keys as
// its read-only footprint, returning the source sequence it was built on and
// the simulation (which carries the real resources and fee).
func (d *Deployer) simulateExtend(ctx context.Context, keys []xdr.LedgerKey, extendTo uint32, deadline time.Time) (int64, protocolrpc.SimulateTransactionResponse, error) {
	var none protocolrpc.SimulateTransactionResponse
	source, err := d.getSourceAccount(ctx)
	if err != nil {
		return 0, none, fmt.Errorf("failed to get source account: %w", err)
	}
	draft, err := d.buildExtendTransaction(source.Sequence, extendTo, xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{Footprint: xdr.LedgerFootprint{ReadOnly: keys}},
	}, deadline)
	if err != nil {
		return 0, none, err
	}
	draftXDR, err := draft.Base64()
	if err != nil {
		return 0, none, fmt.Errorf("failed to get extend transaction XDR: %w", err)
	}
	sim, err := d.rpcClient.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{Transaction: draftXDR})
	if err != nil {
		return 0, none, fmt.Errorf("extend simulation failed: %w", err)
	}
	if sim.Error != "" {
		return 0, none, fmt.Errorf("extend simulation error: %s", sim.Error)
	}
	return source.Sequence, sim, nil
}

func (d *Deployer) buildExtendTransaction(seq int64, extendTo uint32, data xdr.SorobanTransactionData, deadline time.Time) (*txnbuild.Transaction, error) {
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &txnbuild.SimpleAccount{AccountID: d.signer.Address(), Sequence: seq},
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{&txnbuild.ExtendFootprintTtl{
			ExtendTo:      extendTo,
			SourceAccount: d.signer.Address(),
			Ext:           xdr.TransactionExt{V: 1, SorobanData: &data},
		}},
		// Same rule as assembleTransaction: the resource fee is paid via
		// SorobanData.ResourceFee; BaseFee stays at the inclusion minimum.
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, deadline.Unix())},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build extend transaction: %w", err)
	}
	return tx, nil
}

// maxEntryTTL reads the network's maximum entry TTL from its state archival settings.
func (d *Deployer) maxEntryTTL(ctx context.Context) (uint32, error) {
	data, err := d.fetchLedgerEntry(ctx, xdr.LedgerKey{
		Type:          xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.LedgerKeyConfigSetting{ConfigSettingId: xdr.ConfigSettingIdConfigSettingStateArchival},
	})
	if err != nil {
		return 0, fmt.Errorf("fetch state archival settings: %w", err)
	}
	cs, ok := data.GetConfigSetting()
	if !ok || cs.StateArchivalSettings == nil {
		return 0, errors.New("state archival settings missing from config setting entry")
	}
	return uint32(cs.StateArchivalSettings.MaxEntryTtl), nil
}

func (d *Deployer) liveUntilLedgers(ctx context.Context, keys []xdr.LedgerKey) ([]uint32, error) {
	keyXDRs := make([]string, len(keys))
	for i, k := range keys {
		s, err := k.MarshalBinaryBase64()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal ledger key: %w", err)
		}
		keyXDRs[i] = s
	}
	resp, err := d.rpcClient.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{Keys: keyXDRs})
	if err != nil {
		return nil, fmt.Errorf("read back TTLs: %w", err)
	}
	byKey := make(map[string]uint32, len(resp.Entries))
	for _, e := range resp.Entries {
		if e.LiveUntilLedgerSeq != nil {
			byKey[e.KeyXDR] = *e.LiveUntilLedgerSeq
		}
	}
	out := make([]uint32, len(keys))
	for i, k := range keyXDRs {
		ttl, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("ledger entry %s not found after extend", k)
		}
		out[i] = ttl
	}
	return out, nil
}

// ErrLedgerEntryNotFound reports a ledger key with no live entry on chain.
var ErrLedgerEntryNotFound = errors.New("ledger entry not found")

func (d *Deployer) fetchLedgerEntry(ctx context.Context, key xdr.LedgerKey) (xdr.LedgerEntryData, error) {
	keyXDR, err := key.MarshalBinaryBase64()
	if err != nil {
		return xdr.LedgerEntryData{}, fmt.Errorf("failed to marshal ledger key: %w", err)
	}
	resp, err := d.rpcClient.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{Keys: []string{keyXDR}})
	if err != nil {
		return xdr.LedgerEntryData{}, err
	}
	if len(resp.Entries) == 0 {
		return xdr.LedgerEntryData{}, ErrLedgerEntryNotFound
	}
	entryXDR, ok := getLedgerEntryXDR(resp.Entries[0])
	if !ok {
		return xdr.LedgerEntryData{}, errors.New("ledger entry has no data")
	}
	var data xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(entryXDR, &data); err != nil {
		return xdr.LedgerEntryData{}, fmt.Errorf("failed to decode ledger entry: %w", err)
	}
	return data, nil
}

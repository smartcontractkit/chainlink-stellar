package deployment

import (
	"context"
	"errors"
	"fmt"
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

// ExtendTTLToMax extends the given persistent ledger entries to the network's
// maximum TTL in one ExtendFootprintTtl transaction paid by the deployer, and
// returns each entry's live-until ledger afterwards, in key order. Extending is
// permissionless: no contract auth is involved.
//
// Extending a contract instance key does not extend its code; pass
// ContractCodeLedgerKey of the instance's WASM hash as well.
//
// Archived entries are restored first when auto-restore is enabled.
func (d *Deployer) ExtendTTLToMax(ctx context.Context, keys []xdr.LedgerKey) ([]uint32, error) {
	if len(keys) == 0 {
		return nil, errors.New("no ledger keys to extend")
	}
	maxTTL, err := d.maxEntryTTL(ctx)
	if err != nil {
		return nil, err
	}
	// ExtendTo is relative to the current ledger and must stay below MaxEntryTtl.
	extendTo := maxTTL - 1

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
	tx, err := d.buildExtendTransaction(seq, extendTo, sorobanData, deadline)
	if err != nil {
		return nil, err
	}

	if _, err := d.signSubmitAndWait(ctx, tx, deadline, "extend "); err != nil {
		return nil, fmt.Errorf("extend transaction failed: %w", err)
	}
	return d.liveUntilLedgers(ctx, keys)
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
		return xdr.LedgerEntryData{}, errors.New("ledger entry not found")
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

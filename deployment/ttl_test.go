package deployment

import (
	"context"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

const testMaxEntryTTL = 3_110_400

// ttlLedger serves GetLedgerEntries by key: the signer account, the state
// archival config setting, and whatever entries a test registers.
type ttlLedger struct {
	t       *testing.T
	account func(context.Context, protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	entries map[string]protocolrpc.LedgerEntryResult
}

func newTTLLedger(t *testing.T, d *Deployer, seq *int64) *ttlLedger {
	t.Helper()
	l := &ttlLedger{
		t:       t,
		account: mockAccountLedgerEntry(t, d.signer.Address(), seq),
		entries: map[string]protocolrpc.LedgerEntryResult{},
	}
	l.set(xdr.LedgerKey{
		Type:          xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.LedgerKeyConfigSetting{ConfigSettingId: xdr.ConfigSettingIdConfigSettingStateArchival},
	}, &xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.ConfigSettingEntry{
			ConfigSettingId:       xdr.ConfigSettingIdConfigSettingStateArchival,
			StateArchivalSettings: &xdr.StateArchivalSettings{MaxEntryTtl: testMaxEntryTTL},
		},
	}, nil)
	return l
}

func (l *ttlLedger) set(key xdr.LedgerKey, data *xdr.LedgerEntryData, liveUntil *uint32) {
	k, err := key.MarshalBinaryBase64()
	require.NoError(l.t, err)
	var r protocolrpc.LedgerEntryResult
	if data != nil {
		r.DataXDR, err = xdr.MarshalBase64(*data)
		require.NoError(l.t, err)
	}
	r.LiveUntilLedgerSeq = liveUntil
	l.entries[k] = r
}

func (l *ttlLedger) get(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
	var resp protocolrpc.GetLedgerEntriesResponse
	for _, k := range req.Keys {
		var key xdr.LedgerKey
		require.NoError(l.t, xdr.SafeUnmarshalBase64(k, &key))
		if key.Type == xdr.LedgerEntryTypeAccount {
			return l.account(ctx, req)
		}
		if r, ok := l.entries[k]; ok {
			r.KeyXDR = k
			resp.Entries = append(resp.Entries, r)
		}
	}
	return resp, nil
}

func decodeEnvelope(t *testing.T, b64 string) xdr.TransactionEnvelope {
	t.Helper()
	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(b64, &env))
	return env
}

func extendSim(t *testing.T, keys []xdr.LedgerKey, fee int64) protocolrpc.SimulateTransactionResponse {
	t.Helper()
	data, err := xdr.MarshalBase64(xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Footprint: xdr.LedgerFootprint{ReadOnly: keys}},
		ResourceFee: xdr.Int64(fee),
	})
	require.NoError(t, err)
	return protocolrpc.SimulateTransactionResponse{MinResourceFee: fee, TransactionDataXDR: data}
}

// extendFixture wires a deployer whose ledger holds a contract instance and its
// code, returning the keys to extend in instance, code order.
func extendFixture(t *testing.T) (*Deployer, *mockRPC, *ttlLedger, []xdr.LedgerKey) {
	t.Helper()
	mock := &mockRPC{}
	d := newTestDeployer(t, mock)
	seq := testInitialSeq
	ledger := newTTLLedger(t, d, &seq)
	mock.GetLedgerEntriesFn = ledger.get

	instanceKey, err := ContractInstanceLedgerKey(randomContractID(t))
	require.NoError(t, err)
	keys := []xdr.LedgerKey{instanceKey, ContractCodeLedgerKey(xdr.Hash{0x01})}
	instanceLive, codeLive := uint32(900), uint32(800)
	ledger.set(keys[0], nil, &instanceLive)
	ledger.set(keys[1], nil, &codeLive)
	return d, mock, ledger, keys
}

func TestExtendTTLToMax_Success(t *testing.T) {
	d, mock, _, keys := extendFixture(t)

	var simulated, sent []xdr.TransactionEnvelope
	mock.SimulateTransactionFn = func(_ context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		simulated = append(simulated, decodeEnvelope(t, req.Transaction))
		return extendSim(t, keys, 50_000), nil
	}
	mock.SendTransactionFn = func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sent = append(sent, decodeEnvelope(t, req.Transaction))
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}
	mock.GetTransactionFn = func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		return successGetTxResponse(t), nil
	}

	got, err := d.ExtendTTLToMax(context.Background(), keys)
	require.NoError(t, err)
	require.Equal(t, []uint32{900, 800}, got, "live-until is read back in key order")

	require.Len(t, simulated, 1)
	require.Equal(t, keys, simulated[0].V1.Tx.Ext.SorobanData.Resources.Footprint.ReadOnly,
		"the draft declares the keys as its read-only footprint")

	require.Len(t, sent, 1)
	tx := sent[0].V1.Tx
	op := tx.Operations[0].Body.MustExtendFootprintTtlOp()
	require.Equal(t, uint32(testMaxEntryTTL-1), uint32(op.ExtendTo), "ExtendTo must stay below MaxEntryTtl")
	require.Equal(t, xdr.Int64(50_000+12_500), tx.Ext.SorobanData.ResourceFee, "simulated fee plus the 25% bump, counted once")
	require.Equal(t, xdr.SequenceNumber(testInitialSeq+1), tx.SeqNum)
	require.Len(t, sent[0].V1.Signatures, 1)
}

func TestExtendTTLToMax_RestoresArchivedEntries(t *testing.T) {
	d, mock, _, keys := extendFixture(t)

	sims := 0
	mock.SimulateTransactionFn = func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		sims++
		sim := extendSim(t, keys, 50_000)
		if sims == 1 {
			sim.RestorePreamble = &protocolrpc.RestorePreamble{TransactionDataXDR: testSorobanDataB64(t), MinResourceFee: 20_000}
		}
		return sim, nil
	}
	var sentOps []xdr.OperationType
	mock.SendTransactionFn = func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sentOps = append(sentOps, decodeEnvelope(t, req.Transaction).V1.Tx.Operations[0].Body.Type)
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}
	mock.GetTransactionFn = func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		return successGetTxResponse(t), nil
	}

	_, err := d.ExtendTTLToMax(context.Background(), keys)
	require.NoError(t, err)
	require.Equal(t, []xdr.OperationType{xdr.OperationTypeRestoreFootprint, xdr.OperationTypeExtendFootprintTtl}, sentOps)
}

func TestExtendTTLToMax_AutoRestoreDisabled(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	d.autoRestore = false
	mock.SimulateTransactionFn = func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		sim := extendSim(t, keys, 50_000)
		sim.RestorePreamble = &protocolrpc.RestorePreamble{TransactionDataXDR: testSorobanDataB64(t)}
		return sim, nil
	}

	_, err := d.ExtendTTLToMax(context.Background(), keys)
	require.ErrorContains(t, err, "auto-restore is disabled")
}

func TestExtendTTLToMax_TransactionFailed(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	mock.SimulateTransactionFn = func(_ context.Context, _ protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		return extendSim(t, keys, 50_000), nil
	}
	mock.SendTransactionFn = func(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		return protocolrpc.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: "boom"}, nil
	}

	_, err := d.ExtendTTLToMax(context.Background(), keys)
	require.ErrorContains(t, err, "extend transaction rejected: boom")
}

func TestExtendTTLToMax_NoKeys(t *testing.T) {
	d := newTestDeployer(t, &mockRPC{})
	_, err := d.ExtendTTLToMax(context.Background(), nil)
	require.Error(t, err)
}

// atTargetLedger wires a fixed latest-ledger sequence into the mock.
func atTargetLedger(t *testing.T, mock *mockRPC, latestLedger uint32) {
	t.Helper()
	mock.GetLatestLedgerFn = func(context.Context) (protocolrpc.GetLatestLedgerResponse, error) {
		return protocolrpc.GetLatestLedgerResponse{Sequence: latestLedger}, nil
	}
}

func TestExtendTTLTo_Success(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	const latestLedger = uint32(1_000)
	const target = uint32(1_000_000)
	atTargetLedger(t, mock, latestLedger)

	var simulated, sent []xdr.TransactionEnvelope
	mock.SimulateTransactionFn = func(_ context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		simulated = append(simulated, decodeEnvelope(t, req.Transaction))
		return extendSim(t, keys, 50_000), nil
	}
	mock.SendTransactionFn = func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sent = append(sent, decodeEnvelope(t, req.Transaction))
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}
	mock.GetTransactionFn = func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
		return successGetTxResponse(t), nil
	}

	got, err := d.ExtendTTLTo(context.Background(), keys, target)
	require.NoError(t, err)
	require.Equal(t, []uint32{900, 800}, got)

	require.Len(t, simulated, 1)
	require.Equal(t, keys, simulated[0].V1.Tx.Ext.SorobanData.Resources.Footprint.ReadOnly)

	require.Len(t, sent, 1)
	op := sent[0].V1.Tx.Operations[0].Body.MustExtendFootprintTtlOp()
	require.Equal(t, target, uint32(op.ExtendTo), "ExtendTo equals the requested target TTL")
	require.Equal(t, xdr.Int64(50_000+12_500), sent[0].V1.Tx.Ext.SorobanData.ResourceFee)
}

func TestExtendTTLTo_AlreadyAtTargetSendsNothing(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	const latestLedger = uint32(500)
	const target = uint32(300)
	atTargetLedger(t, mock, latestLedger)

	var calls int
	mock.SimulateTransactionFn = func(context.Context, protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		calls++
		return extendSim(t, keys, 50_000), nil
	}
	mock.SendTransactionFn = func(context.Context, protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		calls++
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}

	got, err := d.ExtendTTLTo(context.Background(), keys, target)
	require.NoError(t, err)
	require.Equal(t, []uint32{900, 800}, got, "live-until is still reported in key order")
	require.Zero(t, calls, "no transaction is built or sent when every entry already meets the target")
}

func TestExtendTTLTo_TargetMustStayBelowMax(t *testing.T) {
	d, _, _, keys := extendFixture(t)
	for _, target := range []uint32{testMaxEntryTTL, testMaxEntryTTL + 1} {
		_, err := d.ExtendTTLTo(context.Background(), keys, target)
		require.ErrorContains(t, err, "must stay below the network maximum")
	}
}

func TestExtendTTLTo_FeeOverflowsUint32Cap(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	atTargetLedger(t, mock, 1_000)

	var sent int
	mock.SimulateTransactionFn = func(context.Context, protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		return extendSim(t, keys, 4_000_000_000), nil
	}
	mock.SendTransactionFn = func(context.Context, protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sent++
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}

	_, err := d.ExtendTTLTo(context.Background(), keys, 1_000_000)
	require.ErrorContains(t, err, "exceeds the uint32 transaction fee cap")
	require.Zero(t, sent, "nothing is submitted when the buffered fee cannot fit a regular transaction")
}

func TestSimulateExtendTTL(t *testing.T) {
	d, mock, _, keys := extendFixture(t)
	const latestLedger = uint32(100)
	const target = uint32(1_000_000)
	atTargetLedger(t, mock, latestLedger)

	var sent int
	mock.SimulateTransactionFn = func(context.Context, protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		return extendSim(t, keys, 50_000), nil
	}
	mock.SendTransactionFn = func(context.Context, protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
		sent++
		return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "abc"}, nil
	}

	fee, atTarget, err := d.SimulateExtendTTL(context.Background(), keys, target)
	require.NoError(t, err)
	require.False(t, atTarget)
	require.Equal(t, int64(50_000), fee)
	require.Zero(t, sent, "simulation never submits")

	// Same entries, target already met: fee 0, atTarget true.
	fee, atTarget, err = d.SimulateExtendTTL(context.Background(), keys, 400)
	require.NoError(t, err)
	require.True(t, atTarget)
	require.Zero(t, fee)
}

func TestContractWasmHash(t *testing.T) {
	mock := &mockRPC{}
	d := newTestDeployer(t, mock)
	seq := testInitialSeq
	ledger := newTTLLedger(t, d, &seq)
	mock.GetLedgerEntriesFn = ledger.get

	contractID := randomContractID(t)
	key, err := ContractInstanceLedgerKey(contractID)
	require.NoError(t, err)
	hash := xdr.Hash{0x42}
	ledger.set(key, &xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   key.ContractData.Contract,
			Key:        key.ContractData.Key,
			Durability: xdr.ContractDataDurabilityPersistent,
			Val: xdr.ScVal{
				Type: xdr.ScValTypeScvContractInstance,
				Instance: &xdr.ScContractInstance{Executable: xdr.ContractExecutable{
					Type:     xdr.ContractExecutableTypeContractExecutableWasm,
					WasmHash: &hash,
				}},
			},
		},
	}, nil)

	got, err := d.ContractWasmHash(context.Background(), contractID)
	require.NoError(t, err)
	require.Equal(t, hash, got)

	_, err = d.ContractWasmHash(context.Background(), randomContractID(t))
	require.ErrorContains(t, err, "not found")

	_, err = d.ContractWasmHash(context.Background(), "not-a-contract")
	require.Error(t, err)
}

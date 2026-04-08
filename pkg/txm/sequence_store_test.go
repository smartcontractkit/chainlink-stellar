package txm

import (
	"context"
	"encoding/base64"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/internal/mocks"
)

const testAddress = "GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7"

func makeAccountEntryXDR(t *testing.T, seqNum int64) string {
	t.Helper()
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.AccountEntry{
			AccountId: xdr.MustAddress(testAddress),
			SeqNum:    xdr.SequenceNumber(seqNum),
		},
	}
	raw, err := entry.MarshalBinary()
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

func mustAccountKeyXDR() string {
	key := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: xdr.MustAddress(testAddress),
		},
	}
	b, _ := key.MarshalBinaryBase64()
	return b
}

func setupSeqStore(t *testing.T, onChainSeq int64) (*SequenceStore, *mocks.MockRPCClient) {
	t.Helper()
	m := mocks.NewMockRPCClient(t)
	ss := NewSequenceStore(m, testAddress)

	entryXDR := makeAccountEntryXDR(t, onChainSeq)
	keyXDR := mustAccountKeyXDR()
	m.On("GetLedgerEntries", context.Background(), protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	}).Return(protocolrpc.GetLedgerEntriesResponse{
		Entries: []protocolrpc.LedgerEntryResult{
			{DataXDR: entryXDR},
		},
	}, nil).Maybe()

	return ss, m
}

func TestSequenceStore_NextSequence_LazyFetch(t *testing.T) {
	ss, _ := setupSeqStore(t, 100)

	acct, seq, err := ss.NextSequence(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(101), seq)
	assert.Equal(t, testAddress, acct.AccountID)
	assert.Equal(t, int64(100), acct.Sequence, "base = allocated-1 for txnbuild")
}

func TestSequenceStore_NextSequence_Increments(t *testing.T) {
	ss, _ := setupSeqStore(t, 100)
	ctx := context.Background()

	_, seq1, _ := ss.NextSequence(ctx)
	_, seq2, _ := ss.NextSequence(ctx)
	_, seq3, _ := ss.NextSequence(ctx)

	assert.Equal(t, int64(101), seq1)
	assert.Equal(t, int64(102), seq2)
	assert.Equal(t, int64(103), seq3)
}

func TestSequenceStore_FailedSeqReuse(t *testing.T) {
	ss, _ := setupSeqStore(t, 100)
	ctx := context.Background()

	_, seq1, _ := ss.NextSequence(ctx)
	_, seq2, _ := ss.NextSequence(ctx)
	assert.Equal(t, int64(101), seq1)
	assert.Equal(t, int64(102), seq2)

	ss.Confirm(seq1, false)
	ss.Confirm(seq2, false)

	_, reused1, _ := ss.NextSequence(ctx)
	_, reused2, _ := ss.NextSequence(ctx)
	assert.Equal(t, int64(101), reused1, "should reuse failed seq")
	assert.Equal(t, int64(102), reused2, "should reuse failed seq")

	_, seq3, _ := ss.NextSequence(ctx)
	assert.Equal(t, int64(103), seq3, "should advance after reuse")
}

func TestSequenceStore_Release(t *testing.T) {
	ss, _ := setupSeqStore(t, 100)
	ctx := context.Background()

	_, seq, _ := ss.NextSequence(ctx)
	ss.Release(seq)

	_, reused, _ := ss.NextSequence(ctx)
	assert.Equal(t, seq, reused, "released sequence should be reused")
}

func TestSequenceStore_Confirm_Consumed(t *testing.T) {
	ss, _ := setupSeqStore(t, 100)
	ctx := context.Background()

	_, seq, _ := ss.NextSequence(ctx)
	ss.AddUnconfirmed(seq, "hash1")
	assert.Equal(t, 1, ss.UnconfirmedCount())

	ss.Confirm(seq, true)
	assert.Equal(t, 0, ss.UnconfirmedCount())

	_, next, _ := ss.NextSequence(ctx)
	assert.Greater(t, next, seq, "consumed seq should not be reused")
}

func TestSequenceStore_Confirm_NotConsumed_BelowOnChain(t *testing.T) {
	ss, _ := setupSeqStore(t, 200)
	ctx := context.Background()

	_, seq, _ := ss.NextSequence(ctx)
	ss.Confirm(seq, true)

	ss.Confirm(50, false)

	_, next, _ := ss.NextSequence(ctx)
	assert.Greater(t, next, seq, "expired seq below on-chain should not be reused")
}

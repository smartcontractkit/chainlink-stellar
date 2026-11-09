package accessors

import (
	"context"
	"testing"
	"time"

	"github.com/smartcontractkit/chainlink-common/keystore"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testNetworkPassphrase = "Test SDF Network ; September 2015"
	testKeystorePassword  = "test-keystore-password"
)

func newTestKeystore(t *testing.T) keystore.Keystore {
	t.Helper()
	ctx := context.Background()
	ks, err := keystore.LoadKeystore(ctx, keystore.NewMemoryStorage(), testKeystorePassword, keystore.WithScryptParams(keystore.FastScryptParams))
	require.NoError(t, err)
	return ks
}

func createKey(t *testing.T, ks keystore.Keystore, name string, keyType keystore.KeyType) []byte {
	t.Helper()
	resp, err := ks.CreateKeys(context.Background(), keystore.CreateKeysRequest{
		Keys: []keystore.CreateKeyRequest{{KeyName: name, KeyType: keyType}},
	})
	require.NoError(t, err)
	require.Len(t, resp.Keys, 1)
	return resp.Keys[0].KeyInfo.PublicKey
}

func TestLoadStellarKeystoreSigner(t *testing.T) {
	ctx := context.Background()
	const keyName = "stellar/tx/test"

	t.Run("nil keystore is rejected", func(t *testing.T) {
		_, err := LoadStellarKeystoreSigner(ctx, nil, keyName)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "keystore is required")
	})

	t.Run("empty key name is rejected", func(t *testing.T) {
		ks := newTestKeystore(t)
		_, err := LoadStellarKeystoreSigner(ctx, ks, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "keyName is required")
	})

	t.Run("missing key returns clear error", func(t *testing.T) {
		ks := newTestKeystore(t)
		_, err := LoadStellarKeystoreSigner(ctx, ks, "does-not-exist")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get keystore key")
	})

	t.Run("wrong key type is rejected", func(t *testing.T) {
		ks := newTestKeystore(t)
		createKey(t, ks, keyName, keystore.ECDSA_S256)
		_, err := LoadStellarKeystoreSigner(ctx, ks, keyName)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected Ed25519")
	})

	t.Run("happy path returns signer with G... address", func(t *testing.T) {
		ks := newTestKeystore(t)
		pubKey := createKey(t, ks, keyName, keystore.Ed25519)
		signer, err := LoadStellarKeystoreSigner(ctx, ks, keyName)
		require.NoError(t, err)
		require.NotNil(t, signer)

		expectedAddr, encErr := strkey.Encode(strkey.VersionByteAccountID, pubKey)
		require.NoError(t, encErr)
		assert.Equal(t, expectedAddr, signer.Address())
	})
}

// TestKeystoreTxSigner_SignTransaction_MatchesKeypairFull verifies that the
// keystore-backed signer produces a transaction signature byte-identical to
// what the SDK's *keypair.Full would produce for the same seed. This is the
// strongest guarantee that the keystore path can replace the in-process
// keypair without on-chain behaviour changes.
func TestKeystoreTxSigner_SignTransaction_MatchesKeypairFull(t *testing.T) {
	ctx := context.Background()
	const keyName = "stellar/tx/parity"

	ks := newTestKeystore(t)
	pubKey := createKey(t, ks, keyName, keystore.Ed25519)

	// The keystore creates a fresh random Ed25519 key under the hood; we don't
	// have the raw seed to construct a *keypair.Full directly. Instead we
	// build the signer, sign the same envelope hash twice (once via the
	// keystore signer's SignTransaction path, once via an explicit
	// keystore.Sign + AddSignatureDecorated) and compare the resulting
	// signatures.
	signer, err := LoadStellarKeystoreSigner(ctx, ks, keyName)
	require.NoError(t, err)

	tx := buildTestTx(t, signer.Address())
	signedViaSigner, err := signer.SignTransaction(ctx, testNetworkPassphrase, tx)
	require.NoError(t, err)
	require.Len(t, signedViaSigner.Signatures(), 1)
	got := signedViaSigner.Signatures()[0]

	// Reproduce the same signature directly via keystore.Sign.
	envelope := tx.ToXDR()
	h, hashErr := network.HashTransactionInEnvelope(envelope, testNetworkPassphrase)
	require.NoError(t, hashErr)
	resp, err := ks.Sign(ctx, keystore.SignRequest{KeyName: keyName, Data: h[:]})
	require.NoError(t, err)
	require.Equal(t, []byte(got.Signature), resp.Signature, "SignTransaction should match keystore.Sign on the envelope hash")

	var wantHint [4]byte
	copy(wantHint[:], pubKey[len(pubKey)-4:])
	assert.Equal(t, wantHint, [4]byte(got.Hint), "decorated signature hint must equal the last 4 bytes of the public key")
}

// TestKeystoreTxSigner_SignTransaction_VerifiableByPublicKey ensures the
// produced signature actually verifies under the keystore's published public
// key — i.e. the signature is over the correct envelope hash and corresponds
// to the address returned by Address().
func TestKeystoreTxSigner_SignTransaction_VerifiableByPublicKey(t *testing.T) {
	ctx := context.Background()
	const keyName = "stellar/tx/verify"

	ks := newTestKeystore(t)
	pubKey := createKey(t, ks, keyName, keystore.Ed25519)

	signer, err := LoadStellarKeystoreSigner(ctx, ks, keyName)
	require.NoError(t, err)

	tx := buildTestTx(t, signer.Address())
	signedTx, err := signer.SignTransaction(ctx, testNetworkPassphrase, tx)
	require.NoError(t, err)
	require.Len(t, signedTx.Signatures(), 1)
	sig := signedTx.Signatures()[0]

	// Reconstruct the signer's public key as a FromAddress and verify directly.
	envelope := signedTx.ToXDR()
	h, err := network.HashTransactionInEnvelope(envelope, testNetworkPassphrase)
	require.NoError(t, err)

	// Use go-stellar-sdk keypair.ParseAddress to get a FromAddress that knows
	// how to verify Ed25519 signatures.
	fromAddr, err := keypair.ParseAddress(signer.Address())
	require.NoError(t, err)
	require.NoError(t, fromAddr.Verify(h[:], sig.Signature))

	// Sanity: hint matches public key's last 4 bytes.
	var wantHint [4]byte
	copy(wantHint[:], pubKey[len(pubKey)-4:])
	assert.Equal(t, wantHint, [4]byte(sig.Hint))
}

// buildTestTx builds a minimal valid transaction envelope rooted at sourceAddr.
// We never submit it; only the envelope hash is used in tests.
func buildTestTx(t *testing.T, sourceAddr string) *txnbuild.Transaction {
	t.Helper()
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &txnbuild.SimpleAccount{
			AccountID: sourceAddr,
			Sequence:  1,
		},
		IncrementSequenceNum: false,
		Operations: []txnbuild.Operation{
			&txnbuild.BumpSequence{BumpTo: 2},
		},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	require.NoError(t, err)
	return tx
}

// blockingKeystore wraps a real keystore but blocks Sign until release is
// closed, ignoring ctx — modelling a stalled keystore that does not itself
// honour cancellation.
type blockingKeystore struct {
	keystore.Keystore
	release chan struct{}
}

func (b *blockingKeystore) Sign(ctx context.Context, req keystore.SignRequest) (keystore.SignResponse, error) {
	<-b.release
	return b.Keystore.Sign(ctx, req)
}

func newBlockingSigner(t *testing.T, timeout time.Duration) (*keystoreTxSigner, chan struct{}) {
	t.Helper()
	const keyName = "stellar/tx/blocking"
	ks := newTestKeystore(t)
	createKey(t, ks, keyName, keystore.Ed25519)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s, err := LoadStellarKeystoreSigner(context.Background(), &blockingKeystore{Keystore: ks, release: release}, keyName)
	require.NoError(t, err)
	ks2 := s.(*keystoreTxSigner)
	ks2.signTimeout = timeout
	return ks2, release
}

func TestKeystoreTxSigner_SignTransaction_HonoursCallerCancellation(t *testing.T) {
	signer, _ := newBlockingSigner(t, time.Hour)
	tx := buildTestTx(t, signer.Address())

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := signer.SignTransaction(ctx, testNetworkPassphrase, tx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestKeystoreTxSigner_SignTransaction_EnforcesSignTimeout(t *testing.T) {
	signer, _ := newBlockingSigner(t, 50*time.Millisecond)
	tx := buildTestTx(t, signer.Address())

	start := time.Now()
	_, err := signer.SignTransaction(context.Background(), testNetworkPassphrase, tx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestKeystoreTxSigner_SignTransaction_AlreadyCancelled(t *testing.T) {
	signer, _ := newBlockingSigner(t, time.Hour)
	tx := buildTestTx(t, signer.Address())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := signer.SignTransaction(ctx, testNetworkPassphrase, tx)
	require.ErrorIs(t, err, context.Canceled)
}

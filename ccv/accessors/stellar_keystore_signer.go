package accessors

import (
	"context"
	"fmt"
	"time"

	"github.com/smartcontractkit/chainlink-common/keystore"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// keystoreTxSigner is a deployment.TxSigner backed by a chainlink-common
// keystore. The Ed25519 private key never leaves the keystore: the transaction
// envelope is hashed locally, the keystore signs the hash, and the resulting
// 64-byte signature is appended to the transaction as a DecoratedSignature.
//
// This mirrors what (*txnbuild.Transaction).Sign does internally, but routes
// the actual signing call through keystore.Signer.Sign so production binaries
// can keep their key material isolated.
type keystoreTxSigner struct {
	ks          keystore.Keystore
	keyName     string
	address     string
	publicKey   []byte
	signTimeout time.Duration
}

// DefaultKeystoreSignTimeout bounds a single keystore Sign call. It is applied
// on top of the caller's context so that a stalled keystore cannot block
// transaction signing indefinitely, even if the caller's context carries no
// deadline. Signing a 32-byte hash is expected to take milliseconds; this is a
// generous upper bound, well below the Deployer's transaction time bound.
const DefaultKeystoreSignTimeout = 10 * time.Second

var _ deployment.TxSigner = (*keystoreTxSigner)(nil)

// LoadStellarKeystoreSigner constructs a deployment.TxSigner whose Ed25519
// private key lives in the given chainlink-common keystore. It validates that
// the requested key exists and is of type keystore.Ed25519, and pre-computes
// the Stellar G... address so callers can synchronously fund / log it without
// another keystore round-trip.
func LoadStellarKeystoreSigner(ctx context.Context, ks keystore.Keystore, keyName string) (deployment.TxSigner, error) {
	if ks == nil {
		return nil, fmt.Errorf("keystore is required")
	}
	if keyName == "" {
		return nil, fmt.Errorf("keyName is required")
	}
	resp, err := ks.GetKeys(ctx, keystore.GetKeysRequest{KeyNames: []string{keyName}})
	if err != nil {
		return nil, fmt.Errorf("get keystore key %q: %w", keyName, err)
	}
	if len(resp.Keys) != 1 {
		return nil, fmt.Errorf("expected exactly one keystore key for %q, got %d", keyName, len(resp.Keys))
	}
	info := resp.Keys[0].KeyInfo
	if info.KeyType != keystore.Ed25519 {
		return nil, fmt.Errorf("keystore key %q has type %s, expected %s", keyName, info.KeyType, keystore.Ed25519)
	}
	if len(info.PublicKey) != 32 {
		return nil, fmt.Errorf("keystore key %q has invalid Ed25519 public key length %d (want 32)", keyName, len(info.PublicKey))
	}

	address, err := strkey.Encode(strkey.VersionByteAccountID, info.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("encode Stellar address from public key: %w", err)
	}

	pk := make([]byte, len(info.PublicKey))
	copy(pk, info.PublicKey)
	return &keystoreTxSigner{
		ks:          ks,
		keyName:     keyName,
		address:     address,
		publicKey:   pk,
		signTimeout: DefaultKeystoreSignTimeout,
	}, nil
}

// Address implements deployment.TxSigner.
func (s *keystoreTxSigner) Address() string { return s.address }

// SignTransaction implements deployment.TxSigner. It hashes the transaction
// envelope, asks the keystore to Ed25519-sign the hash, and returns a clone of
// tx with a new DecoratedSignature appended. The signing path is identical to
// (*keypair.Full).SignDecorated, just routed through the keystore Signer.
//
// The keystore call is bounded by ctx (so transmission cancellation and
// service shutdown propagate) and additionally by signTimeout, whichever
// expires first.
func (s *keystoreTxSigner) SignTransaction(ctx context.Context, networkPassphrase string, tx *txnbuild.Transaction) (*txnbuild.Transaction, error) {
	envelope := tx.ToXDR()
	h, err := network.HashTransactionInEnvelope(envelope, networkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("hash transaction envelope: %w", err)
	}
	if s.signTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.signTimeout)
		defer cancel()
	}
	signResp, err := s.sign(ctx, h[:])
	if err != nil {
		return nil, fmt.Errorf("keystore sign with %q: %w", s.keyName, err)
	}

	hint := s.hint()
	decorated := xdr.NewDecoratedSignature(signResp.Signature, hint)
	signed, err := tx.AddSignatureDecorated(decorated)
	if err != nil {
		return nil, fmt.Errorf("attach decorated signature: %w", err)
	}
	return signed, nil
}

// sign invokes the keystore and returns as soon as either the keystore responds
// or ctx is done. Not every keystore implementation honours ctx (the in-memory
// chainlink-common keystore, for instance, can block on its internal lock), so
// the call runs in its own goroutine and ctx is enforced here. If ctx wins, the
// goroutine is abandoned and exits once the keystore eventually returns; the
// buffered channel guarantees it never blocks on send.
func (s *keystoreTxSigner) sign(ctx context.Context, data []byte) (keystore.SignResponse, error) {
	if err := ctx.Err(); err != nil {
		return keystore.SignResponse{}, err
	}
	type result struct {
		resp keystore.SignResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := s.ks.Sign(ctx, keystore.SignRequest{KeyName: s.keyName, Data: data})
		done <- result{resp: resp, err: err}
	}()
	select {
	case r := <-done:
		return r.resp, r.err
	case <-ctx.Done():
		return keystore.SignResponse{}, ctx.Err()
	}
}

// hint mirrors (*keypair.Full).Hint: the last 4 bytes of the Ed25519 public key.
// Stellar uses the hint to pick the right signer when multiple keys could sign
// for an account; for a single-signer account it is purely informational.
func (s *keystoreTxSigner) hint() [4]byte {
	var h [4]byte
	copy(h[:], s.publicKey[len(s.publicKey)-4:])
	return h
}

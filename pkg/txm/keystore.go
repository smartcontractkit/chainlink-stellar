package txm

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Keystore signs Stellar transactions. In production this delegates to
// Chainlink's keystore infrastructure; for tests a simple keypair adapter
// is provided.
type Keystore interface {
	// Sign signs the transaction with the key identified by account address.
	Sign(tx *txnbuild.Transaction, networkPassphrase string) (*txnbuild.Transaction, error)
	// SignerAddress returns the G… strkey of the signing account.
	SignerAddress() string
}

// KeypairKeystore is a Keystore backed by a single *keypair.Full.
// Used in the CCIP path where ed25519 signing is done directly.
type KeypairKeystore struct {
	kp *keypair.Full
}

// NewKeypairKeystore wraps a Stellar keypair as a Keystore.
func NewKeypairKeystore(kp *keypair.Full) *KeypairKeystore {
	return &KeypairKeystore{kp: kp}
}

func (k *KeypairKeystore) Sign(tx *txnbuild.Transaction, networkPassphrase string) (*txnbuild.Transaction, error) {
	return tx.Sign(networkPassphrase, k.kp)
}

func (k *KeypairKeystore) SignerAddress() string {
	return k.kp.Address()
}

// LoopKeystore is the minimal interface for Chainlink's keystore service
// (CRE path). In production this is satisfied by loop.Keystore from
// chainlink-common. Defined here to avoid a hard dependency on
// chainlink-common/pkg/loop in the TXM package.
type LoopKeystore interface {
	Sign(ctx context.Context, id string, data []byte) ([]byte, error)
}

// LoopKeystoreSigner implements Keystore by delegating to a LoopKeystore.
// It computes the transaction signing hash, calls the keystore service, and
// attaches the resulting ed25519 signature to the transaction envelope.
type LoopKeystoreSigner struct {
	ks      LoopKeystore
	keyID   string
	address string
}

// NewLoopKeystoreSigner creates a Keystore backed by a LoopKeystore.
// keyID is the identifier used to look up the signing key in the keystore.
// address is the Stellar G… address corresponding to that key.
func NewLoopKeystoreSigner(ks LoopKeystore, keyID, address string) *LoopKeystoreSigner {
	return &LoopKeystoreSigner{ks: ks, keyID: keyID, address: address}
}

func (s *LoopKeystoreSigner) Sign(tx *txnbuild.Transaction, networkPassphrase string) (*txnbuild.Transaction, error) {
	hash, err := tx.Hash(networkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("compute signing hash: %w", err)
	}

	sigBytes, err := s.ks.Sign(context.Background(), s.keyID, hash[:])
	if err != nil {
		return nil, fmt.Errorf("keystore sign: %w", err)
	}

	kp, err := keypair.ParseAddress(s.address)
	if err != nil {
		return nil, fmt.Errorf("parse signer address: %w", err)
	}

	sig := xdr.DecoratedSignature{
		Hint:      xdr.SignatureHint(kp.Hint()),
		Signature: xdr.Signature(sigBytes),
	}

	return tx.AddSignatureDecorated(sig)
}

func (s *LoopKeystoreSigner) SignerAddress() string {
	return s.address
}

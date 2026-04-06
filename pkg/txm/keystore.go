package txm

import (
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
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

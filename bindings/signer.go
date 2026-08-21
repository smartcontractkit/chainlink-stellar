package bindings

import "github.com/stellar/go-stellar-sdk/xdr"

// Signer is the common signing surface for Stellar transactions.
// It supports local keypairs and remote signers that do not expose
// private key material.
type Signer interface {
	Address() string
	SignDecorated([]byte) (xdr.DecoratedSignature, error)
}

package bindings

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Signer provides signing capabilities for Stellar transactions.
type Signer interface {
	// Sign signs the given message and returns the signature bytes.
	Sign(message []byte) ([]byte, error)

	// SignDecorated signs the given message and returns a decorated signature
	// in Stellar XDR format.
	SignDecorated(message []byte) (xdr.DecoratedSignature, error)

	// Address returns the Stellar address derived from the signer's public key.
	Address() string

	// KeypairFull returns the underlying keypair.Full used by this signer,
	// if available.
	KeypairFull() *keypair.Full
}

// stellarKeypairSigner implements Signer using a keypair.Full from the
// Stellar SDK.
type stellarKeypairSigner struct {
	kp *keypair.Full
}

var _ Signer = (*stellarKeypairSigner)(nil)

// NewStellarKeypairSigner creates a new Signer backed by a keypair.Full.
func NewStellarKeypairSigner(kp *keypair.Full) Signer {
	return &stellarKeypairSigner{
		kp: kp,
	}
}

// Sign signs the given message and returns the signature bytes.
func (s *stellarKeypairSigner) Sign(message []byte) ([]byte, error) {
	return s.kp.Sign(message)
}

// SignDecorated signs the given message and returns a decorated Stellar
// signature.
func (s *stellarKeypairSigner) SignDecorated(
	message []byte,
) (xdr.DecoratedSignature, error) {
	return s.kp.SignDecorated(message)
}

// Address returns the Stellar account address.
func (s *stellarKeypairSigner) Address() string {
	return s.kp.Address()
}

// KeypairFull returns the underlying keypair.Full.
func (s *stellarKeypairSigner) KeypairFull() *keypair.Full {
	return s.kp
}

// KeypairFromHex creates a keypair.Full from a hex-encoded Stellar private
// key. The hex string may optionally have a 0x prefix.
func KeypairFromHex(hexKey string) (*keypair.Full, error) {
	hexKey = strings.TrimPrefix(hexKey, "0x")

	rawSeed, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to decode hex key: %w",
			err,
		)
	}

	if len(rawSeed) != 32 {
		return nil, fmt.Errorf(
			"invalid key length: expected 32 bytes, got %d",
			len(rawSeed),
		)
	}

	var seed [32]byte
	copy(seed[:], rawSeed)

	kp, err := keypair.FromRawSeed(seed)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to create keypair from seed: %w",
			err,
		)
	}

	return kp, nil
}

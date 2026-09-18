package deployment

import (
	"testing"

	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

var _ stellarbindings.Signer = (sdkOnlyStellarSigner{})

func TestNewDeployerFromChain_rejectsNilSigner(t *testing.T) {
	t.Parallel()
	ch := cldfstellar.Chain{
		Signer: nil,
	}
	_, err := NewDeployerFromChain(ch)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Signer is nil")
}

func TestNewDeployerFromChain_usesKeypairWhenKeypairFullAvailable(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	ch := cldfstellar.Chain{
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	dep, err := NewDeployerFromChain(ch)
	require.NoError(t, err)
	require.NotNil(t, dep)
	require.Equal(t, kp.Address(), dep.SignerAddress())
}

func TestNewDeployerFromChain_usesSDKSignerWhenKeypairFullNil(t *testing.T) {
	t.Parallel()
	ch := cldfstellar.Chain{
		Signer:            sdkOnlyStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	dep, err := NewDeployerFromChain(ch)
	require.NoError(t, err)
	require.NotNil(t, dep)
	require.Equal(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", dep.SignerAddress())
}

// sdkOnlyStellarSigner implements stellarbindings.Signer with nil KeypairFull,
// matching keystore-style signers that only expose SignDecorated.
type sdkOnlyStellarSigner struct{ addr string }

func (s sdkOnlyStellarSigner) Address() string { return s.addr }

func (sdkOnlyStellarSigner) Sign([]byte) ([]byte, error) { return nil, nil }

func (sdkOnlyStellarSigner) SignDecorated([]byte) (xdr.DecoratedSignature, error) {
	return xdr.DecoratedSignature{}, nil
}

func (sdkOnlyStellarSigner) KeypairFull() *keypair.Full { return nil }

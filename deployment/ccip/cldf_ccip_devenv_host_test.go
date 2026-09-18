package ccip

import (
	"testing"

	"github.com/rs/zerolog"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestNewCLDFStellarCCIPDevenvHost_rejectsNilDeployer(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	ch := cldfstellar.Chain{
		Signer: stellarbindings.NewStellarKeypairSigner(kp),
	}
	_, err := NewCLDFStellarCCIPDevenvHost(ch, zerolog.Nop(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "deployer is nil")
}

func TestNewCLDFStellarCCIPDevenvHost_rejectsNilSigner(t *testing.T) {
	t.Parallel()
	var dep deployment.Deployer
	_, err := NewCLDFStellarCCIPDevenvHost(cldfstellar.Chain{}, zerolog.Nop(), &dep)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Signer is nil")
}

type stellarSignerNilKeypair struct {
	inner stellarbindings.Signer
}

func (s stellarSignerNilKeypair) Sign(message []byte) ([]byte, error) {
	return s.inner.Sign(message)
}

func (s stellarSignerNilKeypair) SignDecorated(message []byte) (xdr.DecoratedSignature, error) {
	return s.inner.SignDecorated(message)
}

func (s stellarSignerNilKeypair) Address() string { return s.inner.Address() }

func (s stellarSignerNilKeypair) KeypairFull() *keypair.Full { return nil }

func TestNewCLDFStellarCCIPDevenvHost_rejectsNilKeypairFull(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	inner := stellarbindings.NewStellarKeypairSigner(kp)
	ch := cldfstellar.Chain{
		Signer: stellarSignerNilKeypair{inner: inner},
	}
	var dep deployment.Deployer
	_, err := NewCLDFStellarCCIPDevenvHost(ch, zerolog.Nop(), &dep)
	require.Error(t, err)
	require.Contains(t, err.Error(), "KeypairFull is nil")
}

var _ stellarbindings.Signer = stellarSignerNilKeypair{}

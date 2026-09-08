package sequences

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
)

type testStellarSigner struct{ addr string }

func (testStellarSigner) Sign([]byte) ([]byte, error) { return nil, nil }
func (testStellarSigner) SignDecorated([]byte) (xdr.DecoratedSignature, error) {
	return xdr.DecoratedSignature{}, nil
}
func (s testStellarSigner) Address() string          { return s.addr }
func (testStellarSigner) KeypairFull() *keypair.Full { return nil }

func TestValidateStellarTimelockAdmin_zeroEVMAddressIsAccepted(t *testing.T) {
	in := deploy.MCMSDeploymentConfigPerChainWithAddress{
		MCMSDeploymentConfigPerChain: deploy.MCMSDeploymentConfigPerChain{
			TimelockAdmin: common.Address{},
		},
	}
	require.NoError(t, validateStellarTimelockAdmin(in))
}

func TestValidateStellarTimelockAdmin_rejectsNonZeroEVMAddress(t *testing.T) {
	in := deploy.MCMSDeploymentConfigPerChainWithAddress{
		MCMSDeploymentConfigPerChain: deploy.MCMSDeploymentConfigPerChain{
			TimelockAdmin: common.HexToAddress("0x00000000000000000000000000000000000000f1"),
		},
	}
	require.Error(t, validateStellarTimelockAdmin(in))
}

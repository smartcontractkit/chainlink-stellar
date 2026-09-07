package sequences

import (
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// testStellarSigner is a no-op cldfstellar.Chain signer that only reports an address.
//
// It previously lived in mcms_timelock_admin_test.go alongside the tests for
// stellarTimelockAdmin. That function was removed when chainlink-ccip dropped
// MCMSDeploymentConfigPerChain.TimelockAdmin (admin transfer moved to the separate
// GrantAdminRoleToTimelock changeset), but transfer_ownership_test.go still needs the signer.
type testStellarSigner struct{ addr string }

func (testStellarSigner) Sign([]byte) ([]byte, error) { return nil, nil }

func (testStellarSigner) SignDecorated([]byte) (xdr.DecoratedSignature, error) {
	return xdr.DecoratedSignature{}, nil
}

func (s testStellarSigner) Address() string { return s.addr }

func (testStellarSigner) KeypairFull() *keypair.Full { return nil }

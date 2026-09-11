// Stellar committee-verifier binary entry point.
//
// The Stellar source-reader wiring lives in ccv/accessors and the verifier-side
// signing key is loaded from the bootstrapper keystore by chainlink-ccv. This
// file only needs to declare which keys the bootstrapper must provision for
// this service: the ECDSA key used to sign verification results, and the
// Stellar Ed25519 key used by the source-reader's deployer account.
package main

import (
	"fmt"

	_ "github.com/lib/pq"

	"github.com/smartcontractkit/chainlink-ccv/bootstrap"
	verifiercmd "github.com/smartcontractkit/chainlink-ccv/cmd/verifier"
	"github.com/smartcontractkit/chainlink-ccv/verifier/pkg/commit"
	"github.com/smartcontractkit/chainlink-common/keystore"

	_ "github.com/smartcontractkit/chainlink-stellar/ccv/accessors" // registers Stellar chainaccess constructor
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
)

func main() {
	if err := bootstrap.Run(
		"StellarCommitteeVerifier",
		verifiercmd.NewCommitteeVerifierServiceFactory(),
		bootstrap.WithKey(commit.DefaultECDSASigningKeyName, "signing", keystore.ECDSA_S256),
		bootstrap.WithKey(common.StellarTransmitterKeyName, "transmitting", keystore.Ed25519),
	); err != nil {
		panic(fmt.Sprintf("failed to run Stellar committee verifier: %s", err.Error()))
	}
}

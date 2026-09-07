// Stellar executor binary entry point.
//
// All per-chain wiring (config loading, OffRamp/RMN address resolution, keystore-backed
// transmitter construction) lives in ccv/accessors. The chainlink-ccv executor service
// pulls DestinationReader and ContractTransmitter directly off the Accessor returned by
// the registry, so this file only needs to declare the bootstrapper-managed keys this
// service requires.
//
// chainlink-ccv devenv always POSTs GetKeys for executor.DefaultEVMTransmitterKeyName
// after the container starts (see build/devenv/services/executor/base.go). That ECDSA key
// is not used for Soroban submission but must exist so bootstrap HTTP does not return 500.
// Soroban signing uses common.StellarTransmitterKeyName (Ed25519).
package main

import (
	"fmt"

	_ "github.com/lib/pq"

	"github.com/smartcontractkit/chainlink-ccv/bootstrap"
	executorcmd "github.com/smartcontractkit/chainlink-ccv/cmd/executor"
	"github.com/smartcontractkit/chainlink-common/keystore"

	_ "github.com/smartcontractkit/chainlink-stellar/ccv/accessors" // registers Stellar chainaccess constructor
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
)

func main() {
	if err := bootstrap.Run(
		"StellarExecutor",
		executorcmd.NewFactory(),
		// bootstrap.WithKey(executor.DefaultEVMTransmitterKeyName, "transmitting", keystore.ECDSA_S256),
		bootstrap.WithKey(common.StellarTransmitterKeyName, "transmitting", keystore.Ed25519),
	); err != nil {
		panic(fmt.Sprintf("failed to run Stellar executor: %s", err.Error()))
	}
}

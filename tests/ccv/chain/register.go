// Package ccvchain implements the Stellar chain for chainlink-ccv devenv (runtime wiring lives
// in other files in this package).
//
// Registration relationship with deployment/adapters/init.go:
//
//   - That package’s init registers Stellar with chainlink-ccip CCIP 2.0 tooling, shared
//     registries (MCMS, fees, tokens, curse), and chainlink-ccv/deployment/adapters.
//
//   - This file registers chainlink-ccv/build/devenv-only hooks via RegisterStellarDevenvComponents
//     (modifiers, ImplFactory, CLDF provider, extra-args serializers, chain config loader).
//
// This package blank-imports deployment/adapters so its init runs as soon as ccvchain loads.
// Loading ccvchain does not call RegisterStellarDevenvComponents; devenv mains and tests must
// invoke RegisterStellarDevenvComponents or RegisterStellarComponents for those hooks.
package ccvchain

import (
	"sync"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvccipevm "github.com/smartcontractkit/chainlink-ccv/build/devenv/evm"

	modifier "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain/modifier"

	// Triggers deployment/adapters init; see package doc.
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
)

var registerOnce sync.Once

// RegisterStellarDevenvComponents registers Stellar with chainlink-ccv **build/devenv**
// (chain config loader, modifiers, ImplFactory, CLDF provider, extra-args serializers).
// It does not replace deployment/adapters init; see package doc.
func RegisterStellarDevenvComponents() {
	registerOnce.Do(func() {
		chainreg.Register(chainsel.FamilyStellar, chainreg.Registration{
			ImplFactory:       NewImplFactory(),
			ExecutorInfo:      NewImplFactory(),
			CLDFProvider:      NewCLDFProviderFactory(),
			ChainConfigLoader: StellarChainConfigLoader,
			VerifierModifier:  modifier.StellarVerifierModifier,
			ExecutorModifier:  modifier.StellarExecutorModifier,
			ExtraArgsSerializers: map[uint8]chainreg.ExtraArgsSerializer{
				1: devenvccipevm.BuildEVMExtraArgsV1,
				2: devenvccipevm.BuildEVMExtraArgsV2,
				3: devenvccipevm.SerializeMessageV3ExtraArgs,
			},
		})
	})
}

// RegisterStellarComponents is an alias for [RegisterStellarDevenvComponents].
func RegisterStellarComponents() {
	RegisterStellarDevenvComponents()
}

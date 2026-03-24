package devenv

import (
	"fmt"

	"github.com/Masterminds/semver/v3"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccvevmadapters "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/adapters"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/registry"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/chainconfig"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"

	ccvchain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	"github.com/smartcontractkit/chainlink-stellar/ccv/devenv/modifier"
)

// RegisterStellarComponents registers all Stellar-specific devenv components with
// the global registries. Call this in init() of any entry point (CLI command or E2E test)
// that needs to operate on Stellar chains.
//
// This registers:
//   - CommitteeVerifierModifier: customises the verifier Docker container for Stellar.
//   - ExecutorModifier:          customises the executor Docker container for Stellar.
//   - ChainConfigLoader:         provides placeholder blockchain info for Stellar chains.
//   - LaneAdapter (Stellar):     wraps the EVM v2.0.0 lane adapter for Stellar address decoding.
//   - ImplFactory:               factory for creating Stellar CCIP17 chain implementations.
//   - CLDFProviderFactory:       factory for creating Stellar CLDF BlockChain providers.
func RegisterStellarComponents() {
	laneV2 := semver.MustParse("2.0.0")
	evmLane, ok := lanes.GetLaneAdapterRegistry().GetLaneAdapter(chainsel.FamilyEVM, laneV2)
	if !ok {
		panic("EVM lane adapter (2.0.0) not registered; import github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/adapters before calling RegisterStellarComponents")
	}
	evmBase, ok := evmLane.(*ccvevmadapters.ChainFamilyAdapter)
	if !ok {
		panic(fmt.Sprintf("EVM lane adapter has unexpected type %T; expected *adapters.ChainFamilyAdapter", evmLane))
	}

	chainconfig.RegisterChainConfigLoader(chainsel.FamilyStellar, StellarChainConfigLoader)
	committeeverifier.RegisterModifier(chainsel.FamilyStellar, modifier.StellarVerifierModifier)
	services.RegisterExecutorModifier(chainsel.FamilyStellar, modifier.StellarExecutorModifier)
	ccv.RegisterImplFactory(chainsel.FamilyStellar, ccvchain.NewImplFactory())
	lanes.GetLaneAdapterRegistry().RegisterLaneAdapter(chainsel.FamilyStellar, laneV2, ccvchain.NewChainFamilyAdapter(evmBase))
	registry.RegisterCLDFProviderFactory(chainsel.FamilyStellar, ccvchain.NewCLDFProviderFactory())
}

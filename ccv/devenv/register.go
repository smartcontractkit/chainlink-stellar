package devenv

import (
	"sync"

	"github.com/Masterminds/semver/v3"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	ccvdevmevm "github.com/smartcontractkit/chainlink-ccv/build/devenv/evm"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/registry"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/chainconfig"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/executor"

	ccvchain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	"github.com/smartcontractkit/chainlink-stellar/ccv/devenv/adapter"
	"github.com/smartcontractkit/chainlink-stellar/ccv/devenv/modifier"
)

var registerOnce sync.Once

// RegisterStellarComponents registers all Stellar-specific devenv components with
// the global registries. Safe to call multiple times (idempotent via sync.Once).
//
// This registers:
//   - CommitteeVerifierModifier: customises the verifier Docker container for Stellar.
//   - ExecutorModifier:          customises the executor Docker container for Stellar.
//   - ChainConfigLoader:         provides placeholder blockchain info for Stellar chains.
//   - ImplFactory:               factory for creating Stellar CCIP17 chain implementations.
//   - CLDFProviderFactory:       factory for creating Stellar CLDF BlockChain providers.
func RegisterStellarComponents() {
	registerOnce.Do(func() {
		// EVM SendMessage encodes extraArgs via destination family; Stellar uses the same
		// ABI GenericExtraArgsV1–V3 wire format from the EVM router (lane defaults supply CCV/executor).
		cciptestinterfaces.RegisterExtraArgsSerializer(chainsel.FamilyStellar, ccvdevmevm.SerializeEVMExtraArgs)

		chainconfig.RegisterChainConfigLoader(chainsel.FamilyStellar, StellarChainConfigLoader)
		committeeverifier.RegisterModifier(chainsel.FamilyStellar, modifier.StellarVerifierModifier)
		executor.RegisterModifier(chainsel.FamilyStellar, modifier.StellarExecutorModifier)
		ccv.RegisterImplFactory(chainsel.FamilyStellar, ccvchain.NewImplFactory())
		registry.RegisterCLDFProviderFactory(chainsel.FamilyStellar, ccvchain.NewCLDFProviderFactory())
		ccvadapters.GetCommitteeVerifierContractRegistry().Register(chainsel.FamilyStellar, &adapter.StellarCommitteeVerifierContractAdapter{})
		stellarAdapter := &adapter.StellarLaneAdapter{}
		lanes.GetLaneAdapterRegistry().RegisterLaneAdapter(chainsel.FamilyStellar, semver.MustParse("2.0.0"), stellarAdapter)
		ccvadapters.GetChainFamilyRegistry().RegisterChainFamily(chainsel.FamilyStellar, stellarAdapter)
		ccvadapters.GetAggregatorConfigRegistry().Register(chainsel.FamilyStellar, &adapter.StellarAggregatorConfigAdapter{})
		ccvadapters.GetIndexerConfigRegistry().Register(chainsel.FamilyStellar, &adapter.StellarIndexerConfigAdapter{})
		ccvadapters.GetVerifierJobConfigRegistry().Register(chainsel.FamilyStellar, &adapter.StellarVerifierConfigAdapter{})
		ccvadapters.GetExecutorConfigRegistry().Register(chainsel.FamilyStellar, &adapter.StellarExecutorConfigAdapter{})
		ccvadapters.GetTokenVerifierConfigRegistry().Register(chainsel.FamilyStellar, &adapter.StellarTokenVerifierConfigAdapter{})
		ccvadapters.GetDeployChainContractsRegistry().Register(chainsel.FamilyStellar, &ccvchain.StellarDeployChainContractsAdapter{})
	})
}

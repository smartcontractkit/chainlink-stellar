package ccvchain

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

var _ chainreg.AddressResolver = AddressResolver{}

// AddressResolver implements [chainreg.AddressResolver] for Stellar chains,
// resolving tcapi test-case handles (receiver, executor, committee CCV, token)
// from the CLDF datastore. Datastore rows store raw 32-byte Soroban contract
// IDs as 0x-prefixed hex, so addresses are returned as raw bytes via
// protocol.NewUnknownAddressFromHex.
type AddressResolver struct{}

// resolveRefAddress loads the datastore row for ref and decodes it to a raw address.
func resolveRefAddress(ds datastore.DataStore, chainSelector uint64, ref stellarccip.DatastoreSorobanContractRef, contractName string) (protocol.UnknownAddress, error) {
	addrRef, err := ref.LookupAddressRef(ds, chainSelector)
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("failed to get %s address for chain selector %d, ContractType: %s, Qualifier: %q: %w",
			contractName, chainSelector, ref.Type, ref.Qualifier, err)
	}
	return protocol.NewUnknownAddressFromHex(addrRef.Address)
}

// GetContractReceiver implements [chainreg.AddressResolver].
// Stellar deploys a single ccip_receiver example contract (no per-qualifier
// mock receivers exist), so every qualifier maps to it.
func (AddressResolver) GetContractReceiver(ds datastore.DataStore, chainSelector uint64, qualifier string) (protocol.UnknownAddress, error) {
	return resolveRefAddress(ds, chainSelector, stellarccip.CCIPReceiverDatastoreRef(), "ccip receiver")
}

// GetExecutor implements [chainreg.AddressResolver].
// Returns the executor proxy row for the given qualifier; only the "default"
// pool is deployed in devenv today, so other qualifiers error and the
// requesting test case skips.
func (AddressResolver) GetExecutor(ds datastore.DataStore, chainSelector uint64, qualifier string) (protocol.UnknownAddress, error) {
	return resolveRefAddress(ds, chainSelector, stellarccip.ExecutorProxyDatastoreRef(qualifier), "executor proxy")
}

// GetCommitteeCCV implements [chainreg.AddressResolver].
// Returns the committee verifier resolver (VVR) proxy row for the requested
// qualifier; absent qualifiers (e.g. "secondary") error and the test case skips.
func (AddressResolver) GetCommitteeCCV(ds datastore.DataStore, chainSelector uint64, qualifier string) (protocol.UnknownAddress, error) {
	vvr := stellarccip.VVRDatastoreRef()
	vvr.Qualifier = qualifier
	return resolveRefAddress(ds, chainSelector, vvr, "committee verifier proxy")
}

// GetToken implements [chainreg.AddressResolver].
// tcapi token-transfer combos identify pools by chain-agnostic catalog names
// (devenvcommon pool types), so map on those: lock-release pools wrap the
// single deterministic SAC test token; burn-mint pools wrap the BnM token.
// The pool qualifier is ignored (the catalog's instance qualifiers don't match
// the stellar datastore's). Anything else errors so the test case skips.
func (AddressResolver) GetToken(ds datastore.DataStore, chainSelector uint64, poolRef datastore.AddressRef) (protocol.UnknownAddress, error) {
	var tokenRef stellarccip.DatastoreSorobanContractRef
	switch string(poolRef.Type) {
	case devenvcommon.LockReleaseTokenPoolType:
		tokenRef = stellarccip.DevenvTestTokenDatastoreRef()
	case devenvcommon.BurnMintTokenPoolType:
		tokenRef = stellarccip.BnmTokenDatastoreRef()
	default:
		return protocol.UnknownAddress{}, fmt.Errorf("no stellar token mapped for pool type %s qualifier %q", poolRef.Type, poolRef.Qualifier)
	}
	return resolveRefAddress(ds, chainSelector, tokenRef, "token")
}

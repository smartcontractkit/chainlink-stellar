package ccip

import (
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

// GetOnRampStrkey resolves the local OnRamp contract strkey for a chain from the datastore.
func GetOnRampStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return OnRampDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetOffRampStrkey resolves the local OffRamp contract strkey for a chain from the datastore.
func GetOffRampStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return OffRampDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetRouterStrkey resolves the local Router contract strkey for a chain from the datastore.
func GetRouterStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return RouterDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetFeeQuoterStrkey resolves the FeeQuoter contract strkey for a chain from the datastore.
func GetFeeQuoterStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return FeeQuoterDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetTokenAdminRegistryStrkey resolves TokenAdminRegistry for a chain from the datastore.
func GetTokenAdminRegistryStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return TokenAdminRegistryDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetVVRStrkey resolves the Versioned Verifier Resolver strkey for a chain from the datastore.
func GetVVRStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return VVRDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetCommitteeVerifierStrkey resolves the Committee Verifier strkey for a chain from the datastore.
func GetCommitteeVerifierStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return CommitteeVerifierDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetRampRegistryStrkey resolves the RampRegistry strkey for a chain from the datastore.
func GetRampRegistryStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return RampRegistryDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetRMNRemoteStrkey resolves RMN Remote strkey for a chain from the datastore.
func GetRMNRemoteStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return RMNRemoteDatastoreRef().LookupStrkey(ds, chainSelector)
}

// GetRMNProxyStrkey resolves the RMN Proxy strkey for a chain from the datastore.
// Pools store the RMN proxy immutably at initialize (EVM `immutable i_rmnProxy` parity),
// so post-deploy pool initialization must rehydrate it from the datastore.
func GetRMNProxyStrkey(ds datastore.DataStore, chainSelector uint64) (string, error) {
	return RMNProxyDatastoreRef().LookupStrkey(ds, chainSelector)
}

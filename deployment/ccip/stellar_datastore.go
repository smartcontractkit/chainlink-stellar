package ccip

import (
	"fmt"

	"github.com/Masterminds/semver/v3"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

// MergeExistingAddressRefs upserts CCIP / environment address refs into the
// in-memory deploy datastore (EVM-style ExistingAddresses seeding).
func MergeExistingAddressRefs(ds *datastore.MemoryDataStore, refs []datastore.AddressRef) error {
	for _, ref := range refs {
		if err := ds.AddressRefStore.Upsert(ref); err != nil {
			return fmt.Errorf("merge existing address ref (%s %s): %w", ref.Type, ref.Version, err)
		}
	}
	return nil
}

// UpsertDeployedStrKey records a deployed Soroban contract strkey in the datastore.
func UpsertDeployedStrKey(
	ds *datastore.MemoryDataStore,
	chainSelector uint64,
	typ datastore.ContractType,
	version *semver.Version,
	qualifier string,
	contractStrkey string,
) error {
	hexAddr, err := stellarutil.StrkeyToHex(contractStrkey)
	if err != nil {
		return fmt.Errorf("strkey to hex for %s: %w", typ, err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       hexAddr,
		ChainSelector: chainSelector,
		Type:          typ,
		Version:       version,
		Qualifier:     qualifier,
	}); err != nil {
		return fmt.Errorf("upsert address ref %s: %w", typ, err)
	}
	return nil
}

// RecordOnRamp records an OnRamp deployment in the datastore.
func RecordOnRamp(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return OnRampDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordOffRamp records an OffRamp deployment in the datastore.
func RecordOffRamp(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return OffRampDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordRouter records a Router deployment in the datastore.
func RecordRouter(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return RouterDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordFeeQuoter records a FeeQuoter deployment in the datastore.
func RecordFeeQuoter(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return FeeQuoterDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordRMNRemote records an RMN Remote deployment in the datastore.
func RecordRMNRemote(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return RMNRemoteDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordRMNProxy records an RMN Proxy deployment in the datastore. Pools store the RMN
// proxy immutably at initialize (EVM `immutable i_rmnProxy` parity), so post-deploy pool
// initialization rehydrates it from the datastore via GetRMNProxyStrkey.
func RecordRMNProxy(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return RMNProxyDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordTokenAdminRegistry records TokenAdminRegistry in the datastore.
func RecordTokenAdminRegistry(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordVVR records the Versioned Verifier Resolver in the datastore.
func RecordVVR(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return VVRDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordCommitteeVerifier records the Committee Verifier in the datastore.
func RecordCommitteeVerifier(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return CommitteeVerifierDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordRampRegistry records the RampRegistry in the datastore.
func RecordRampRegistry(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return RampRegistryDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordCCIPReceiver records the CCIP receiver example in the datastore.
func RecordCCIPReceiver(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return CCIPReceiverDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordLockReleasePool records the legacy lock-release pool in the datastore.
func RecordLockReleasePool(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return LegacyLockReleasePoolDevenvDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordSiloedLockReleasePool records the siloed lock-release pool in the datastore.
func RecordSiloedLockReleasePool(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return SiloedLockReleasePoolDevenvDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// RecordTokenLockBox records the token lock box in the datastore.
func RecordTokenLockBox(ds *datastore.MemoryDataStore, chainSelector uint64, contractStrkey string) error {
	return TokenLockBoxDevenvDatastoreRef().UpsertDeployedStrKey(ds, chainSelector, contractStrkey)
}

// LockReleasePoolAddressRefDataStore returns a sealed datastore containing the
// legacy lock-release pool AddressRef and, when tokenContractID is non-empty, the
// test token AddressRef for devenv (qualifier DevenvLegacyLockReleasePoolQualifier).
func LockReleasePoolAddressRefDataStore(chainSelector uint64, poolContractID, tokenContractID string) (datastore.DataStore, error) {
	ds := datastore.NewMemoryDataStore()
	poolHex, err := stellarutil.StrkeyToHex(poolContractID)
	if err != nil {
		return nil, fmt.Errorf("convert pool address: %w", err)
	}
	poolRef := LegacyLockReleasePoolDevenvDatastoreRef()
	if err := ds.AddressRefStore.Add(poolRef.FullAddressRef(chainSelector, poolHex)); err != nil {
		return nil, fmt.Errorf("add pool address ref: %w", err)
	}
	if tokenContractID != "" {
		tokenHex, err := stellarutil.StrkeyToHex(tokenContractID)
		if err != nil {
			return nil, fmt.Errorf("convert token address: %w", err)
		}
		tokenRef := DevenvTestTokenDatastoreRef()
		if err := ds.AddressRefStore.Add(tokenRef.FullAddressRef(chainSelector, tokenHex)); err != nil {
			return nil, fmt.Errorf("add token address ref: %w", err)
		}
	}
	return ds.Seal(), nil
}

// DevenvTokenPoolsAddressRefDataStore returns a sealed datastore with siloed pool (E2E),
// legacy lock-release pool, token lock box, and optional test token refs for devenv.
func DevenvTokenPoolsAddressRefDataStore(
	chainSelector uint64,
	siloedPoolContractID,
	legacyPoolContractID,
	lockBoxContractID,
	tokenContractID string,
) (datastore.DataStore, error) {
	ds := datastore.NewMemoryDataStore()

	addRef := func(ref DatastoreSorobanContractRef, contractID string) error {
		if contractID == "" {
			return nil
		}
		hexAddr, err := stellarutil.StrkeyToHex(contractID)
		if err != nil {
			return fmt.Errorf("convert %s address: %w", ref.Type, err)
		}
		if err := ds.AddressRefStore.Add(ref.FullAddressRef(chainSelector, hexAddr)); err != nil {
			return fmt.Errorf("add %s address ref: %w", ref.Type, err)
		}
		return nil
	}

	if err := addRef(SiloedLockReleasePoolDevenvDatastoreRef(), siloedPoolContractID); err != nil {
		return nil, err
	}
	if err := addRef(LegacyLockReleasePoolDevenvDatastoreRef(), legacyPoolContractID); err != nil {
		return nil, err
	}
	if err := addRef(TokenLockBoxDevenvDatastoreRef(), lockBoxContractID); err != nil {
		return nil, err
	}
	if tokenContractID != "" {
		tokenHex, err := stellarutil.StrkeyToHex(tokenContractID)
		if err != nil {
			return nil, fmt.Errorf("convert token address: %w", err)
		}
		tokenRef := DevenvTestTokenDatastoreRef()
		if err := ds.AddressRefStore.Add(tokenRef.FullAddressRef(chainSelector, tokenHex)); err != nil {
			return nil, fmt.Errorf("add token address ref: %w", err)
		}
	}
	return ds.Seal(), nil
}

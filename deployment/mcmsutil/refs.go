package mcmsutil

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
)

// MCMSRole identifies one of the three role-specific Soroban MCMS instances. Each role
// deploys an independent contract with its own signer config, root, nonce, and datastore ref.
type MCMSRole string

const (
	RoleProposer  MCMSRole = "proposer"
	RoleCanceller MCMSRole = "canceller"
	RoleBypasser  MCMSRole = "bypasser"
)

// AllMCMSRoles is the deployment order for the three role-specific MCMS instances.
var AllMCMSRoles = []MCMSRole{RoleProposer, RoleCanceller, RoleBypasser}

// InstanceLabel is the immutable on-chain label passed to MCMS initialize (PROPOSER/CANCELLER/BYPASSER).
func (r MCMSRole) InstanceLabel() string {
	return strings.ToUpper(string(r))
}

// DatastoreType maps a role to its EVM-style datastore contract type. There is deliberately
// no base "MCMS" alias: each role resolves to exactly one address, and a missing role fails closed.
func (r MCMSRole) DatastoreType() (datastore.ContractType, error) {
	switch r {
	case RoleProposer:
		return datastore.ContractType(utils.ProposerManyChainMultisig), nil
	case RoleCanceller:
		return datastore.ContractType(utils.CancellerManyChainMultisig), nil
	case RoleBypasser:
		return datastore.ContractType(utils.BypasserManyChainMultisig), nil
	default:
		return "", fmt.Errorf("unknown MCMS role %q", r)
	}
}

// QualifierStr returns the qualifier string or empty if nil.
func QualifierStr(q *string) string {
	if q == nil {
		return ""
	}
	return *q
}

// ChainNetworkID is SHA-256 of the network passphrase (Soroban MCMS chain_network_id).
func ChainNetworkID(passphrase string) [32]byte {
	return sha256.Sum256([]byte(passphrase))
}

// MCMSRoleDeploySalt derives a deterministic per-role deploy salt so the three role
// instances land at distinct addresses. Domain is versioned (v2) to avoid v1 address reuse.
func MCMSRoleDeploySalt(chainSelector uint64, qual string, role MCMSRole) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("stellar-mcms-v2:%s:%d:%s", role, chainSelector, qual)))
}

// TimelockDeploySalt derives a deterministic deploy salt for a Soroban timelock instance.
func TimelockDeploySalt(chainSelector uint64, qual string) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("stellar-timelock-v2:%d:%s", chainSelector, qual)))
}

// FindExistingStellarMCMSByRole returns the address for exactly one role, or (","false) if absent.
// It never falls back to another role or a base MCMS type — a missing role ref must fail closed.
func FindExistingStellarMCMSByRole(refs []datastore.AddressRef, chainSelector uint64, qual string, role MCMSRole) (string, bool, error) {
	ct, err := role.DatastoreType()
	if err != nil {
		return "", false, err
	}
	v := deploy.MCMSVersion
	for _, r := range refs {
		if r.ChainSelector != chainSelector {
			continue
		}
		if r.Qualifier != qual {
			continue
		}
		if !r.Version.Equal(v) {
			continue
		}
		if r.Type == ct && r.Address != "" {
			return r.Address, true, nil
		}
	}
	return "", false, nil
}

// StellarMCMSRoleDatastoreRef emits the datastore ref for a single role-specific MCMS instance.
func StellarMCMSRoleDatastoreRef(chainSelector uint64, qual string, role MCMSRole, contractID string) (datastore.AddressRef, error) {
	ct, err := role.DatastoreType()
	if err != nil {
		return datastore.AddressRef{}, err
	}
	return datastore.AddressRef{
		ChainSelector: chainSelector,
		Type:          ct,
		Version:       deploy.MCMSVersion,
		Qualifier:     qual,
		Address:       contractID,
	}, nil
}

// FindExistingStellarTimelock returns the RBACTimelock contract id from refs when present.
func FindExistingStellarTimelock(refs []datastore.AddressRef, chainSelector uint64, qual string) (string, bool) {
	v := deploy.MCMSVersion
	for _, r := range refs {
		if r.ChainSelector != chainSelector {
			continue
		}
		if r.Qualifier != qual {
			continue
		}
		if !r.Version.Equal(v) {
			continue
		}
		if r.Type == datastore.ContractType(utils.RBACTimelock) && r.Address != "" {
			return r.Address, true
		}
	}
	return "", false
}

// StellarTimelockDatastoreRef is a single RBACTimelock ref (matches EVM datastore labeling).
func StellarTimelockDatastoreRef(chainSelector uint64, qual, contractID string) datastore.AddressRef {
	return datastore.AddressRef{
		ChainSelector: chainSelector,
		Type:          datastore.ContractType(utils.RBACTimelock),
		Version:       deploy.MCMSVersion,
		Qualifier:     qual,
		Address:       contractID,
	}
}

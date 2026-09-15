package sequences

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	creforwarderops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/cre_forwarder"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// DefaultCREForwarderWasmRelative is the release artifact path relative to the chainlink-stellar repo root.
const DefaultCREForwarderWasmRelative = "target/wasm32v1-none/release/forwarder.wasm"

// ResolveCREForwarderWasmPath returns the path to forwarder.wasm.
// Order: STELLAR_CRE_FORWARDER_WASM (full path), then CHAINLINK_STELLAR_ROOT + DefaultCREForwarderWasmRelative, then cwd-relative.
func ResolveCREForwarderWasmPath() (string, error) {
	if p := os.Getenv("STELLAR_CRE_FORWARDER_WASM"); p != "" {
		return p, nil
	}
	if root := os.Getenv("CHAINLINK_STELLAR_ROOT"); root != "" {
		return filepath.Join(root, DefaultCREForwarderWasmRelative), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, DefaultCREForwarderWasmRelative), nil
}

// CREForwarderDeploySalt derives a deterministic deploy salt for a CRE forwarder instance.
func CREForwarderDeploySalt(chainSelector uint64, qual string) [32]byte {
	return sha256.Sum256(fmt.Appendf(nil, "stellar-cre-forwarder:%d:%s", chainSelector, qual))
}

// StellarCREForwarderDatastoreRef is the datastore ref emitted for a deployed CRE forwarder.
func StellarCREForwarderDatastoreRef(chainSelector uint64, qual, contractID string) datastore.AddressRef {
	return datastore.AddressRef{
		ChainSelector: chainSelector,
		Type:          datastore.ContractType(creforwarderops.ContractType),
		Version:       semver.MustParse(creforwarderops.Deploy.Version()),
		Qualifier:     qual,
		Address:       contractID,
	}
}

// FindExistingStellarCREForwarder returns the CRE forwarder contract id from refs when present.
func FindExistingStellarCREForwarder(refs []datastore.AddressRef, chainSelector uint64, qual string) (string, bool) {
	v := semver.MustParse(creforwarderops.Deploy.Version())
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
		if r.Type == datastore.ContractType(creforwarderops.ContractType) && r.Address != "" {
			return r.Address, true
		}
	}
	return "", false
}

// CREForwarderDONConfig is the initial DON signer configuration applied deployer-signed on a fresh deploy.
type CREForwarderDONConfig struct {
	DonID         uint32     `json:"don_id"`
	ConfigVersion uint32     `json:"config_version"`
	F             uint32     `json:"f"`
	Signers       [][32]byte `json:"signers"`
}

// DeployStellarCREForwarderInput drives an idempotent CRE forwarder deploy on one chain.
type DeployStellarCREForwarderInput struct {
	ChainSelector     uint64                 `json:"chain_selector"`
	Qualifier         *string                `json:"qualifier,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existing_addresses,omitempty"`
	// InitialDONConfig and Transmitters are optional deployer-signed bootstrap applied only on a
	// fresh deploy, before ownership handoff. Post-handoff changes go through MCMS/timelock.
	InitialDONConfig *CREForwarderDONConfig `json:"initial_don_config,omitempty"`
	Transmitters     []string               `json:"transmitters,omitempty"`
}

// DeployStellarCREForwarder deploys and initializes the CRE forwarder owned by the deployer,
// then applies the optional bootstrap DON config and transmitter registry. Ownership handoff to
// the RBACTimelock is deliberately separate: reuse StellarTransferOwnershipViaMCMS and
// StellarAcceptOwnership with the emitted ref, exactly like the CCIP contracts. Reruns are
// idempotent: an already-deployed forwarder is left untouched (changes go through governance).
var DeployStellarCREForwarder = cldfops.NewSequence(
	"stellar-deploy-cre-forwarder",
	stellarops.ContractDeploymentVersion,
	"Deploy and bootstrap the CRE forwarder Soroban contract (deployer-owned until governance handoff)",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in DeployStellarCREForwarderInput) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		qual := mcmsutil.QualifierStr(in.Qualifier)

		dep, err := stellarDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)

		if existing, found := FindExistingStellarCREForwarder(in.ExistingAddresses, in.ChainSelector, qual); found {
			return seqcore.OnChainOutput{Addresses: []datastore.AddressRef{StellarCREForwarderDatastoreRef(in.ChainSelector, qual, existing)}}, nil
		}

		wasm, err := ResolveCREForwarderWasmPath()
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		depOut, err := cldfops.ExecuteOperation(b, creforwarderops.Deploy, deps, stellarops.DeployInput{
			WasmPath: wasm,
			Salt:     CREForwarderDeploySalt(in.ChainSelector, qual),
		})
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("cre forwarder deploy: %w", err)
		}
		cid := depOut.Output.ContractID

		_, err = cldfops.ExecuteOperation(b, creforwarderops.Initialize, deps, creforwarderops.InitializeInput{
			ContractID: cid,
			Owner:      dep.SignerAddress(),
		})
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("cre forwarder initialize: %w", err)
		}

		if cfg := in.InitialDONConfig; cfg != nil {
			_, err = cldfops.ExecuteOperation(b, creforwarderops.SetConfig, deps, creforwarderops.SetConfigInput{
				ContractID:    cid,
				DonID:         cfg.DonID,
				ConfigVersion: cfg.ConfigVersion,
				F:             cfg.F,
				Signers:       cfg.Signers,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("cre forwarder set_config: %w", err)
			}
		}
		for _, transmitter := range in.Transmitters {
			_, err = cldfops.ExecuteOperation(b, creforwarderops.AddForwarder, deps, creforwarderops.AddForwarderInput{
				ContractID: cid,
				Forwarder:  transmitter,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("cre forwarder add_forwarder %s: %w", transmitter, err)
			}
		}

		return seqcore.OnChainOutput{Addresses: []datastore.AddressRef{StellarCREForwarderDatastoreRef(in.ChainSelector, qual, cid)}}, nil
	},
)

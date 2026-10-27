package adapters

import (
	"fmt"
	"slices"

	"github.com/stellar/go-stellar-sdk/strkey"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tpoolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
)

var _ tokens.TokenPoolMigrator = (*StellarTokenAdapter)(nil)

// GetSupportedChains implements tokens.TokenPoolMigrator. The pool has no
// supported-chains enumeration entrypoint (is_supported_chain answers per
// remote chain), so every other chain in the environment is probed — the same
// probing approach the committee-verifier scan takes. tokenAddr is ignored: a
// stellar pool contract alone identifies its remote-chain config.
func (a *StellarTokenAdapter) GetSupportedChains(e deployment.Environment, chainSelector uint64, poolAddr, _ []byte) ([]uint64, error) {
	deps, poolStrkey, err := migratorPoolDeps(e, chainSelector, poolAddr)
	if err != nil {
		return nil, err
	}
	var candidates []uint64
	for sel := range e.BlockChains.All() {
		if sel != chainSelector {
			candidates = append(candidates, sel)
		}
	}
	return probeSupportedChains(e.OperationsBundle, deps, poolStrkey, candidates)
}

// GetRemoteToken implements tokens.TokenPoolMigrator: the remote token bytes
// the pool stores for one remote chain, as stored (the remote family's native
// form). tokenAddr is ignored: a stellar pool contract alone identifies its
// remote-chain config.
func (a *StellarTokenAdapter) GetRemoteToken(e deployment.Environment, chainSelector uint64, poolAddr, _ []byte, remoteSelector uint64) ([]byte, error) {
	deps, poolStrkey, err := migratorPoolDeps(e, chainSelector, poolAddr)
	if err != nil {
		return nil, err
	}
	return readMigrationRemoteToken(e.OperationsBundle, deps, poolStrkey, remoteSelector)
}

// GetRemotePools implements tokens.TokenPoolMigrator: the remote pool entries
// the pool stores for one remote chain, as stored (raw or left-padded-32 wire
// forms). tokenAddr is ignored: a stellar pool contract alone identifies its
// remote-chain config.
func (a *StellarTokenAdapter) GetRemotePools(e deployment.Environment, chainSelector uint64, poolAddr, _ []byte, remoteSelector uint64) ([][]byte, error) {
	deps, poolStrkey, err := migratorPoolDeps(e, chainSelector, poolAddr)
	if err != nil {
		return nil, err
	}
	return readMigrationRemotePools(e.OperationsBundle, deps, poolStrkey, remoteSelector)
}

// probeSupportedChains probes each candidate remote selector for support on
// the pool (unit-tested with a recording invoker) and returns the supported
// ones, sorted.
func probeSupportedChains(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolStrkey string, candidates []uint64) ([]uint64, error) {
	var supported []uint64
	for _, sel := range candidates {
		report, err := cldf_ops.ExecuteOperation(b, tpoolops.IsSupportedChain, deps, tpoolops.IsSupportedChainInput{
			ContractID: poolStrkey, RemoteChainSelector: sel,
		}, cldf_ops.WithForceExecute[tpoolops.IsSupportedChainInput, stellardeps.StellarDeps]())
		if err != nil {
			return nil, fmt.Errorf("probe remote support of pool %s for chain %d: %w", poolStrkey, sel, err)
		}
		if report.Output {
			supported = append(supported, sel)
		}
	}
	slices.Sort(supported)
	return supported, nil
}

// readMigrationRemoteToken reads the remote token bytes the pool stores for
// one remote chain (unit-tested with a recording invoker).
func readMigrationRemoteToken(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolStrkey string, remoteSelector uint64) ([]byte, error) {
	report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemoteToken, deps, tpoolops.GetRemoteTokenInput{
		ContractID: poolStrkey, RemoteChainSelector: remoteSelector,
	}, cldf_ops.WithForceExecute[tpoolops.GetRemoteTokenInput, stellardeps.StellarDeps]())
	if err != nil {
		return nil, fmt.Errorf("read remote token of pool %s for chain %d: %w", poolStrkey, remoteSelector, err)
	}
	return report.Output, nil
}

// readMigrationRemotePools reads the remote pool entries the pool stores for
// one remote chain (unit-tested with a recording invoker).
func readMigrationRemotePools(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolStrkey string, remoteSelector uint64) ([][]byte, error) {
	report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemotePools, deps, tpoolops.GetRemotePoolsInput{
		ContractID: poolStrkey, RemoteChainSelector: remoteSelector,
	}, cldf_ops.WithForceExecute[tpoolops.GetRemotePoolsInput, stellardeps.StellarDeps]())
	if err != nil {
		return nil, fmt.Errorf("read remote pools of pool %s for chain %d: %w", poolStrkey, remoteSelector, err)
	}
	return report.Output, nil
}

// migratorPoolDeps resolves the read deps and the pool strkey from the raw
// on-chain address bytes the migrator interface takes.
func migratorPoolDeps(e deployment.Environment, chainSelector uint64, poolAddr []byte) (stellardeps.StellarDeps, string, error) {
	deps, err := stellarReadDeps(e.BlockChains, chainSelector)
	if err != nil {
		return stellardeps.StellarDeps{}, "", err
	}
	poolStrkey, err := bytesToContractStrkey(poolAddr)
	if err != nil {
		return stellardeps.StellarDeps{}, "", fmt.Errorf("resolve pool address on chain %d: %w", chainSelector, err)
	}
	return deps, poolStrkey, nil
}

// bytesToContractStrkey encodes raw 32-byte on-chain address bytes as the
// contract strkey the pool bindings take.
func bytesToContractStrkey(addr []byte) (string, error) {
	if len(addr) != 32 {
		return "", fmt.Errorf("address bytes must be 32, got %d", len(addr))
	}
	return strkey.Encode(strkey.VersionByteContract, addr)
}

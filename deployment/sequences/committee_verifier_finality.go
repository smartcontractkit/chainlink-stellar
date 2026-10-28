package sequences

import (
	"encoding/binary"
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// StellarSetCommitteeVerifierFinalityInput sets the allowed finality of one Committee Verifier.
type StellarSetCommitteeVerifierFinalityInput struct {
	ChainSelector uint64
	// CommitteeVerifier is the verifier ref; Address must be the contract strkey.
	CommitteeVerifier datastore.AddressRef
	// Timelock is the RBACTimelock strkey allowed to own the verifier.
	Timelock        string
	AllowedFinality finality.Config
}

// StellarSetCommitteeVerifierFinality calls set_allowed_finality_config directly when the
// deployer owns the verifier, and emits it as an MCMS batch op when the timelock does.
var StellarSetCommitteeVerifierFinality = cldfops.NewSequence(
	"stellar-seq-set-committee-verifier-finality",
	stellarops.ContractDeploymentVersion,
	"Sets the Stellar Committee Verifier allowed finality, directly or via MCMS",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarSetCommitteeVerifierFinalityInput) (output seqcore.OnChainOutput, err error) {
		if err := in.AllowedFinality.Validate(); err != nil {
			return output, err
		}
		raw := in.AllowedFinality.Raw()
		allowed := binary.BigEndian.Uint32(raw[:])

		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return output, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
		if err != nil {
			return output, fmt.Errorf("stellar deployer from chain: %w", err)
		}
		deps := stellardeps.FromDeployer(dep)

		owner, err := readContractOwner(b.GetContext(), deps, in.CommitteeVerifier)
		if err != nil {
			return output, fmt.Errorf("read owner of committee verifier %s: %w", in.CommitteeVerifier.Address, err)
		}
		switch owner {
		case dep.SignerAddress():
			_, err := cldfops.ExecuteOperation(b, cvops.SetAllowedFinalityConfig, deps, cvops.SetAllowedFinalityConfigInput{
				ContractID:            in.CommitteeVerifier.Address,
				AllowedFinalityConfig: allowed,
			})
			return output, err
		case in.Timelock:
			data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("set_allowed_finality_config", []xdr.ScVal{scval.Uint32ToScVal(allowed)})
			if err != nil {
				return output, fmt.Errorf("encode set_allowed_finality_config payload: %w", err)
			}
			output.BatchOps = []mcmstypes.BatchOperation{mcmsutil.MCMSInvokeTx(in.CommitteeVerifier, data)}
			return output, nil
		default:
			return output, fmt.Errorf("committee verifier %s owner %q is neither the deployer %q nor the timelock %q",
				in.CommitteeVerifier.Address, owner, dep.SignerAddress(), in.Timelock)
		}
	},
)

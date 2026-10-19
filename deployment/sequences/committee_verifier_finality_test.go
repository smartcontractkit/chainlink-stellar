package sequences

import (
	"errors"
	"testing"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

func finalityTestInput(sel uint64, timelock string, cfg finality.Config) StellarSetCommitteeVerifierFinalityInput {
	cvID := stellarutil.MustGenerateMockContractID("deployer", "cv-finality")
	return StellarSetCommitteeVerifierFinalityInput{
		ChainSelector:     sel,
		CommitteeVerifier: stellarccip.CommitteeVerifierDatastoreRefWithQualifier("mcms-governance-test").FullAddressRef(sel, cvID),
		Timelock:          timelock,
		AllowedFinality:   cfg,
	}
}

func TestStellarSetCommitteeVerifierFinality_ProposesWhenTimelockOwns(t *testing.T) {
	// No t.Parallel: mutates the readContractOwner seam.
	sel := uint64(424242420046)
	timelock := stellarutil.MustGenerateMockContractID("deployer", "cv-finality-timelock")
	stubContractOwner(t, timelock, nil)
	in := finalityTestInput(sel, timelock, finality.Config{WaitForSafe: true, BlockDepth: 5})

	report, err := cldfops.ExecuteSequence(newTestBundle(t), StellarSetCommitteeVerifierFinality, withdrawTestChains(sel), in)
	require.NoError(t, err)
	require.Len(t, report.Output.BatchOps, 1)
	bo := report.Output.BatchOps[0]
	require.EqualValues(t, sel, bo.ChainSelector)
	require.Len(t, bo.Transactions, 1)
	tx := bo.Transactions[0]
	require.Equal(t, in.CommitteeVerifier.Address, tx.To)
	require.Equal(t, "CommitteeVerifier", tx.OperationMetadata.ContractType)
	require.JSONEq(t, `{"family":"stellar","encodingVersion":1}`, string(tx.AdditionalFields))

	fn, args := decodeWithdrawPayload(t, tx.Data)
	require.Equal(t, "set_allowed_finality_config", fn)
	require.Len(t, args, 1)
	got, err := scval.Uint32FromScVal(args[0])
	require.NoError(t, err)
	require.Equal(t, uint32(0x00010005), got)
}

func TestStellarSetCommitteeVerifierFinality_Rejects(t *testing.T) {
	// No t.Parallel: mutates the readContractOwner seam.
	sel := uint64(424242420047)
	timelock := stellarutil.MustGenerateMockContractID("deployer", "cv-finality-timelock")
	for _, tt := range []struct {
		name    string
		owner   string
		readErr error
		cfg     finality.Config
		wantErr string
	}{
		{name: "empty finality", owner: timelock, wantErr: "finality config is empty"},
		{name: "foreign owner", owner: keypair.MustRandom().Address(), cfg: finality.Config{WaitForFinality: true}, wantErr: "is neither the deployer"},
		{name: "owner read error", readErr: errors.New("rpc down"), cfg: finality.Config{WaitForFinality: true}, wantErr: "rpc down"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubContractOwner(t, tt.owner, tt.readErr)
			_, err := cldfops.ExecuteSequence(newTestBundle(t), StellarSetCommitteeVerifierFinality, withdrawTestChains(sel), finalityTestInput(sel, timelock, tt.cfg))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

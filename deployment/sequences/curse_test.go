package sequences

import (
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	api "github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"
)

func contractStrkey(t *testing.T, seed byte) string {
	t.Helper()
	raw := make([]byte, 32)
	raw[0] = seed
	id, err := strkey.Encode(strkey.VersionByteContract, raw)
	require.NoError(t, err)
	return id
}

func TestAuthorizeCurseCaller(t *testing.T) {
	t.Parallel()
	deployer := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	govTL := contractStrkey(t, 1)
	fastTL := contractStrkey(t, 2)
	cclTL := contractStrkey(t, 3)
	strangerTL := contractStrkey(t, 4)
	subjects := [][16]byte{{0xFD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFD}}
	baseInput := func() StellarCurseInput {
		return StellarCurseInput{
			CurseInput: curseAPIInput(subjects, ""),
			Owner:      contractStrkey(t, 5), // an owner that is neither deployer nor any timelock
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
				utils.CLLQualifier:                cclTL,
			},
		}
	}

	t.Run("deployer is owner", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Owner = deployer
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, deployer, caller)
		require.Empty(t, qual, "direct execution has no governing qualifier")
	})
	t.Run("deployer is curse admin", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseAdmins = []string{deployer}
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, deployer, caller)
		require.Empty(t, qual)
	})
	t.Run("explicit qualifier wins over the deployer-direct arm", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Owner = deployer // the deployer could curse directly…
		in.CurseInput = curseAPIInput(subjects, utils.UltraFastCurseMCMSQualifier)
		in.CurseAdmins = []string{fastTL}
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, fastTL, caller, "an explicit qualifier must produce a governed proposal, not a direct sign-and-submit")
		require.Equal(t, utils.UltraFastCurseMCMSQualifier, qual)
	})
	t.Run("explicit qualifier wins when the deployer is a curse admin", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseAdmins = []string{deployer, govTL}
		in.CurseInput = curseAPIInput(subjects, utils.RMNTimelockQualifier)
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, govTL, caller)
		require.Equal(t, utils.RMNTimelockQualifier, qual)
	})
	t.Run("explicit unauthorized qualifier errors even when the deployer is owner", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Owner = deployer
		in.CurseInput = curseAPIInput(subjects, utils.CLLQualifier)
		_, _, err := authorizeCurseCaller(in, deployer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not authorized")
		require.Contains(t, err.Error(), "CLLCCIP")
	})
	t.Run("UltraFastCurse qualifier resolves its timelock", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseInput = curseAPIInput(subjects, utils.UltraFastCurseMCMSQualifier)
		in.CurseAdmins = []string{fastTL}
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, fastTL, caller)
		require.Equal(t, utils.UltraFastCurseMCMSQualifier, qual)
	})
	t.Run("RMNMCMS qualifier resolves the owner timelock", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseInput = curseAPIInput(subjects, utils.RMNTimelockQualifier)
		in.Owner = govTL
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, govTL, caller)
		require.Equal(t, utils.RMNTimelockQualifier, qual)
	})
	t.Run("CLLCCIP qualifier is unauthorized", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseInput = curseAPIInput(subjects, utils.CLLQualifier)
		_, _, err := authorizeCurseCaller(in, deployer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "CLLCCIP")
		require.Contains(t, err.Error(), cclTL, "error must name the unauthorized timelock")
		require.Contains(t, err.Error(), "apply_curse_admin_updates")
	})
	t.Run("unknown qualifier has no timelock", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.CurseInput = curseAPIInput(subjects, "NoSuchStack")
		_, _, err := authorizeCurseCaller(in, deployer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no RBACTimelock deployed")
		require.Contains(t, err.Error(), "NoSuchStack")
	})
	t.Run("empty qualifier with unauthorized deployer suggests governance first", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Owner = govTL
		in.CurseAdmins = []string{fastTL}
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, govTL, caller)
		require.Equal(t, utils.RMNTimelockQualifier, qual, "the pick names the qualifier the fail-closed error should tell the operator to set")
	})
	t.Run("empty qualifier with unauthorized deployer suggests fast curse when governance absent", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		delete(in.Timelocks, utils.RMNTimelockQualifier)
		in.CurseAdmins = []string{fastTL}
		caller, qual, err := authorizeCurseCaller(in, deployer)
		require.NoError(t, err)
		require.Equal(t, fastTL, caller)
		require.Equal(t, utils.UltraFastCurseMCMSQualifier, qual)
	})
	t.Run("stranger owner fails closed with actionable error", func(t *testing.T) {
		t.Parallel()
		in := baseInput()
		in.Owner = strangerTL // a contract address owned by someone else entirely
		_, _, err := authorizeCurseCaller(in, deployer)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no authorized curse caller")
		require.Contains(t, err.Error(), deployer)
		require.Contains(t, err.Error(), strangerTL)
		require.Contains(t, err.Error(), "apply_curse_admin_updates")
	})
}

func TestStellarCurse_ProposesViaQualifierTimelock(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420099)
	govTL := contractStrkey(t, 1)
	fastTL := contractStrkey(t, 2)
	cclTL := contractStrkey(t, 3)
	rmn := contractStrkey(t, 5)
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            testStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})

	subjects := [][16]byte{{0xFD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFD}}

	t.Run("UltraFastCurse proposes with the fast timelock as caller", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput:  curseAPIInput(subjects, utils.UltraFastCurseMCMSQualifier),
			Owner:       govTL,
			CurseAdmins: []string{fastTL},
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		out, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.NoError(t, err)
		require.Len(t, out.Output.BatchOps, 1)
		txs := out.Output.BatchOps[0].Transactions
		require.Len(t, txs, 1)
		require.Equal(t, rmn, txs[0].To)
		require.Equal(t, "RmnRemote", txs[0].ContractType)
		require.JSONEq(t, `{"version":1,"family":"stellar"}`, string(txs[0].AdditionalFields))

		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(txs[0].Data)
		require.NoError(t, err)
		require.Equal(t, "curse", fn)
		require.Len(t, args, 2)
		caller, err := scval.AddressFromScVal(args[0])
		require.NoError(t, err)
		require.Equal(t, fastTL, caller, "caller must be the executing timelock, not the deployer")
	})

	t.Run("RMNMCMS proposes with the governance timelock as caller", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput: curseAPIInput(subjects, utils.RMNTimelockQualifier),
			Owner:      govTL,
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		out, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.NoError(t, err)
		txs := out.Output.BatchOps[0].Transactions
		require.Len(t, txs, 1)
		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(txs[0].Data)
		require.NoError(t, err)
		require.Equal(t, "curse", fn)
		caller, err := scval.AddressFromScVal(args[0])
		require.NoError(t, err)
		require.Equal(t, govTL, caller)
	})

	t.Run("unauthorized qualifier errors at build time", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput:  curseAPIInput(subjects, utils.CLLQualifier),
			Owner:       govTL,
			CurseAdmins: []string{fastTL},
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
				utils.CLLQualifier:                cclTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not authorized")
	})

	t.Run("fail closed with no authorized caller", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput: curseAPIInput(subjects, ""),
			Owner:      contractStrkey(t, 6),
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no authorized curse caller")
	})

	t.Run("empty qualifier with an unauthorized deployer fails closed naming the stack to set", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput: curseAPIInput(subjects, ""),
			Owner:      govTL, // the RMNMCMS timelock is the owner, so it is the auto-pick
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no MCMS qualifier was supplied")
		require.Contains(t, err.Error(), utils.RMNTimelockQualifier, "the error must name the qualifier to set")
		require.Contains(t, err.Error(), govTL, "the error must name the timelock that would execute it")
	})

	t.Run("empty qualifier fallback to fast curse also fails closed naming it", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput:  curseAPIInput(subjects, ""),
			Owner:       contractStrkey(t, 6), // not any deployed timelock
			CurseAdmins: []string{fastTL},
			Timelocks: map[string]string{
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarCurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no MCMS qualifier was supplied")
		require.Contains(t, err.Error(), utils.UltraFastCurseMCMSQualifier)
		require.Contains(t, err.Error(), fastTL)
	})
}

func TestStellarUncurse_OwnerOnlyRouting(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420100)
	govTL := contractStrkey(t, 1)
	fastTL := contractStrkey(t, 2)
	rmn := contractStrkey(t, 5)
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            testStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	subjects := [][16]byte{{0xFD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFD}}

	t.Run("via RMNMCMS proposes owner-only uncurse with no caller arg", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput:  curseAPIInput(subjects, utils.RMNTimelockQualifier),
			Owner:       govTL,
			CurseAdmins: []string{fastTL},
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		out, err := cldf_ops.ExecuteSequence(b, StellarUncurse, chains, in)
		require.NoError(t, err)
		require.Len(t, out.Output.BatchOps, 1)
		txs := out.Output.BatchOps[0].Transactions
		require.Len(t, txs, 1)
		require.Equal(t, rmn, txs[0].To)

		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(txs[0].Data)
		require.NoError(t, err)
		require.Equal(t, "uncurse", fn)
		require.Len(t, args, 1, "uncurse takes no caller argument")
		vec, ok := args[0].GetVec()
		require.True(t, ok)
		require.Len(t, *vec, len(subjects))
	})

	t.Run("via UltraFastCurse is rejected at build time", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput:  curseAPIInput(subjects, utils.UltraFastCurseMCMSQualifier),
			Owner:       govTL,
			CurseAdmins: []string{fastTL},
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarUncurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "fast-uncurse is not supported")
	})

	t.Run("missing owner in input is an error", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput: curseAPIInput(subjects, utils.RMNTimelockQualifier),
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier: govTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarUncurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "owner is unknown")
	})

	t.Run("empty qualifier with a non-owner deployer fails closed naming RMNMCMS", func(t *testing.T) {
		in := StellarCurseInput{
			CurseInput: curseAPIInput(subjects, ""),
			Owner:      govTL, // the RMNMCMS timelock owns the RMN Remote; the deployer does not
			Timelocks: map[string]string{
				utils.RMNTimelockQualifier:        govTL,
				utils.UltraFastCurseMCMSQualifier: fastTL,
			},
		}
		in.RMNContractID = rmn
		in.CurseInput.ChainSelector = sel
		_, err := cldf_ops.ExecuteSequence(b, StellarUncurse, chains, in)
		require.Error(t, err)
		require.Contains(t, err.Error(), "set the MCMS qualifier")
		require.Contains(t, err.Error(), utils.RMNTimelockQualifier, "the error must name the owner stack's qualifier")
	})
}

func TestDirectExecOutput_EmptyOpIsDroppedDownstream(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	out, err := directExecOutput(b, sel, contractStrkey(t, 7), "stellar-direct-curse")
	require.NoError(t, err)
	require.Len(t, out.BatchOps, 1)
	require.Empty(t, out.BatchOps[0].Transactions, "executed writes must produce no transactions so the shared OutputBuilder drops the op")
}

func curseAPIInput(subjects [][16]byte, qualifier string) api.CurseInput {
	return api.CurseInput{
		Subjects:      subjects,
		ChainSelector: 0,
		MCMSQualifier: qualifier,
	}
}

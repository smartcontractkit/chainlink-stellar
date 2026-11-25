package ccip

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

const (
	opFund        = "fund"
	opChangeTrust = "changeTrust"
	opIssue       = "issue"
	opDeploySAC   = "deploySAC"
)

var errInterrupted = errors.New("interrupted")

type fakeSACTokenChain struct {
	accounts   map[string]bool
	trustlines map[string]xdr.Int64
	contracts  map[string]bool
	failOn     string
	writes     []string
	issued     []xdr.Int64
}

func newFakeSACTokenChain() *fakeSACTokenChain {
	return &fakeSACTokenChain{
		accounts:   map[string]bool{},
		trustlines: map[string]xdr.Int64{},
		contracts:  map[string]bool{},
	}
}

func (f *fakeSACTokenChain) write(op string) error {
	if f.failOn == op {
		return errInterrupted
	}
	f.writes = append(f.writes, op)
	return nil
}

func (f *fakeSACTokenChain) AccountExists(_ context.Context, address string) (bool, error) {
	return f.accounts[address], nil
}

func (f *fakeSACTokenChain) TrustlineBalance(_ context.Context, owner string, _ xdr.Asset) (xdr.Int64, bool, error) {
	balance, ok := f.trustlines[owner]
	return balance, ok, nil
}

func (f *fakeSACTokenChain) ContractExists(_ context.Context, contractID string) (bool, error) {
	return f.contracts[contractID], nil
}

func (f *fakeSACTokenChain) FundAccount(address string) error {
	if err := f.write(opFund); err != nil {
		return err
	}
	f.accounts[address] = true
	return nil
}

func (f *fakeSACTokenChain) ChangeTrust(_ context.Context, owner string, _ txnbuild.CreditAsset) error {
	if err := f.write(opChangeTrust); err != nil {
		return err
	}
	f.trustlines[owner] = 0
	return nil
}

func (f *fakeSACTokenChain) Issue(_ context.Context, owner string, _ txnbuild.CreditAsset, units xdr.Int64) error {
	if err := f.write(opIssue); err != nil {
		return err
	}
	f.trustlines[owner] += units
	f.issued = append(f.issued, units)
	return nil
}

func (f *fakeSACTokenChain) DeploySAC(_ context.Context, asset xdr.Asset) (string, error) {
	if err := f.write(opDeploySAC); err != nil {
		return "", err
	}
	id, err := stellardeployment.ComputeSACContractID(network.TestNetworkPassphrase, asset)
	if err != nil {
		return "", err
	}
	f.contracts[id] = true
	return id, nil
}

func testSACTokenSpec(t *testing.T) sacTokenSpec {
	t.Helper()
	logger := zerolog.Nop()
	return sacTokenSpec{
		asset:             txnbuild.CreditAsset{Code: "FEE", Issuer: keypair.MustRandom().Address()},
		owner:             keypair.MustRandom().Address(),
		networkPassphrase: network.TestNetworkPassphrase,
		fundIssuer:        true,
		logger:            &logger,
	}
}

func expectedSACContractID(t *testing.T, s sacTokenSpec) string {
	t.Helper()
	xdrAsset, err := s.asset.ToXDR()
	require.NoError(t, err)
	id, err := stellardeployment.ComputeSACContractID(s.networkPassphrase, xdrAsset)
	require.NoError(t, err)
	return id
}

func TestReconcileDevenvSACToken_FreshChainRunsEveryStepOnce(t *testing.T) {
	chain := newFakeSACTokenChain()
	spec := testSACTokenSpec(t)

	id, err := reconcileDevenvSACToken(context.Background(), chain, spec)

	require.NoError(t, err)
	require.Equal(t, expectedSACContractID(t, spec), id)
	require.Equal(t, []string{opFund, opChangeTrust, opIssue, opDeploySAC}, chain.writes)
	require.Equal(t, devenvSACTargetBalance, chain.trustlines[spec.owner])
}

func TestReconcileDevenvSACToken_RerunAfterInterruptionRepeatsNoCompletedStep(t *testing.T) {
	tests := []struct {
		name           string
		interruptAt    string
		firstRunWrites []string
		rerunWrites    []string
	}{
		{
			name:           "after funding",
			interruptAt:    opChangeTrust,
			firstRunWrites: []string{opFund},
			rerunWrites:    []string{opChangeTrust, opIssue, opDeploySAC},
		},
		{
			name:           "after ChangeTrust",
			interruptAt:    opIssue,
			firstRunWrites: []string{opFund, opChangeTrust},
			rerunWrites:    []string{opIssue, opDeploySAC},
		},
		{
			name:           "after Payment",
			interruptAt:    opDeploySAC,
			firstRunWrites: []string{opFund, opChangeTrust, opIssue},
			rerunWrites:    []string{opDeploySAC},
		},
		{
			name:           "after SAC deployment",
			firstRunWrites: []string{opFund, opChangeTrust, opIssue, opDeploySAC},
			rerunWrites:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain := newFakeSACTokenChain()
			spec := testSACTokenSpec(t)

			chain.failOn = tc.interruptAt
			_, err := reconcileDevenvSACToken(context.Background(), chain, spec)
			if tc.interruptAt != "" {
				require.ErrorIs(t, err, errInterrupted)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.firstRunWrites, chain.writes)

			chain.failOn = ""
			chain.writes = nil
			id, err := reconcileDevenvSACToken(context.Background(), chain, spec)

			require.NoError(t, err)
			require.Equal(t, expectedSACContractID(t, spec), id)
			require.Equal(t, tc.rerunWrites, chain.writes)
			require.Equal(t, devenvSACTargetBalance, chain.trustlines[spec.owner])
		})
	}
}

func TestReconcileDevenvSACToken_IssuesOnlyShortfallToTarget(t *testing.T) {
	tests := []struct {
		name          string
		balance       xdr.Int64
		wantIssued    []xdr.Int64
		wantWrites    []string
		wantFinalHeld xdr.Int64
	}{
		{
			name:          "below target",
			balance:       devenvSACTargetBalance - 1_234_567,
			wantIssued:    []xdr.Int64{1_234_567},
			wantWrites:    []string{opIssue},
			wantFinalHeld: devenvSACTargetBalance,
		},
		{
			name:          "equal to target",
			balance:       devenvSACTargetBalance,
			wantWrites:    nil,
			wantFinalHeld: devenvSACTargetBalance,
		},
		{
			name:          "above target",
			balance:       devenvSACTargetBalance + 1,
			wantWrites:    nil,
			wantFinalHeld: devenvSACTargetBalance + 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain := newFakeSACTokenChain()
			spec := testSACTokenSpec(t)
			chain.accounts[spec.asset.Issuer] = true
			chain.trustlines[spec.owner] = tc.balance
			chain.contracts[expectedSACContractID(t, spec)] = true

			_, err := reconcileDevenvSACToken(context.Background(), chain, spec)

			require.NoError(t, err)
			require.Equal(t, tc.wantWrites, chain.writes)
			require.Equal(t, tc.wantIssued, chain.issued)
			require.Equal(t, tc.wantFinalHeld, chain.trustlines[spec.owner])
		})
	}
}

func TestReconcileDevenvSACToken_SkipsFundingWithoutFriendbot(t *testing.T) {
	chain := newFakeSACTokenChain()
	spec := testSACTokenSpec(t)
	spec.fundIssuer = false

	_, err := reconcileDevenvSACToken(context.Background(), chain, spec)

	require.NoError(t, err)
	require.Equal(t, []string{opChangeTrust, opIssue, opDeploySAC}, chain.writes)
}

func TestDevenvSACTargetBalance_MatchesPriorPaymentAmount(t *testing.T) {
	require.Equal(t, amount.MustParse("10000000000"), devenvSACTargetBalance)
}

package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// fakeTarPoolResolver fakes the TokenAdminRegistry GetPool surface.
type fakeTarPoolResolver struct {
	pools map[string]string // token strkey -> pool strkey
}

func (f fakeTarPoolResolver) GetPool(_ context.Context, token string) (*string, error) {
	if pool, ok := f.pools[token]; ok {
		poolCopy := pool
		return &poolCopy, nil
	}
	return nil, nil
}

// fakeRemotePoolClient fakes the pool remote-pool surface and records removes.
type fakeRemotePoolClient struct {
	configured map[uint64][][]byte
	removed    []struct {
		remote uint64
		addr   []byte
	}
}

func (f *fakeRemotePoolClient) GetRemotePools(_ context.Context, remote uint64) ([][]byte, error) {
	return f.configured[remote], nil
}

func (f *fakeRemotePoolClient) RemoveRemotePool(_ context.Context, remote uint64, addr []byte) error {
	f.removed = append(f.removed, struct {
		remote uint64
		addr   []byte
	}{remote, addr})
	return nil
}

func newSeededTokenPoolOnchainAdapter(t *testing.T, tar fakeTarPoolResolver, pool *fakeRemotePoolClient) *StellarCCVTokenPoolOnchainAdapter {
	t.Helper()
	return &StellarCCVTokenPoolOnchainAdapter{
		newTarClient:  func(bindings.Invoker, string) tarPoolResolver { return tar },
		newPoolClient: func(bindings.Invoker, string) remotePoolClient { return pool },
	}
}

func tokenPoolOnchainInput() ccvdeploymentadapters.RemoveRemotePoolInput {
	return ccvdeploymentadapters.RemoveRemotePoolInput{
		ChainSelector:       42,
		TokenAddress:        "CTOKENAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAPool",
		RemoteChainSelector: 7,
		RemotePoolAddress:   make([]byte, 20), // EVM-style 20-byte remote pool
		ExistingAddresses:   nil,
	}
}

func TestStellarCCVTokenPoolOnchainAdapter_RemoveRemotePool_happyPath(t *testing.T) {
	t.Parallel()
	input := tokenPoolOnchainInput()
	padded := common.LeftPadBytes(input.RemotePoolAddress, 32)
	pool := &fakeRemotePoolClient{configured: map[uint64][][]byte{7: {padded}}}
	a := newSeededTokenPoolOnchainAdapter(t, fakeTarPoolResolver{
		pools: map[string]string{input.TokenAddress: "CPOOAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAL"},
	}, pool)
	tarStrkey := "CTARAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	out, err := a.removeRemotePool(context.Background(), nil, tarStrkey, input)
	require.NoError(t, err)
	// The remove targets the padded on-chain form, once.
	require.Len(t, pool.removed, 1)
	require.Equal(t, input.RemoteChainSelector, pool.removed[0].remote)
	require.Equal(t, padded, pool.removed[0].addr)
	// Deployer-key mode: an ExecInfo-marked empty batch op, dropped downstream.
	require.Len(t, out.BatchOps, 1)
	require.Empty(t, out.BatchOps[0].Transactions)
}

func TestStellarCCVTokenPoolOnchainAdapter_RemoveRemotePool_noPool(t *testing.T) {
	t.Parallel()
	input := tokenPoolOnchainInput()
	a := newSeededTokenPoolOnchainAdapter(t, fakeTarPoolResolver{}, &fakeRemotePoolClient{})
	_, err := a.removeRemotePool(context.Background(), nil, "CTAR", input)
	require.ErrorContains(t, err, "no pool configured for token")
}

func TestStellarCCVTokenPoolOnchainAdapter_RemoveRemotePool_notConfigured(t *testing.T) {
	t.Parallel()
	input := tokenPoolOnchainInput()
	pool := &fakeRemotePoolClient{configured: map[uint64][][]byte{7: {common.LeftPadBytes([]byte{9}, 32)}}}
	a := newSeededTokenPoolOnchainAdapter(t, fakeTarPoolResolver{
		pools: map[string]string{input.TokenAddress: "CPOOAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAL"},
	}, pool)
	_, err := a.removeRemotePool(context.Background(), nil, "CTAR", input)
	require.ErrorContains(t, err, "is not configured for token")
}

func TestResolveTarStrkey(t *testing.T) {
	t.Parallel()
	tar := stellarccip.TokenAdminRegistryDatastoreRef()
	hexAddr := strings.Repeat("ab", 32)
	refs := []datastore.AddressRef{{
		ChainSelector: 42,
		Type:          tar.Type,
		Version:       tar.Version,
		Address:       "0x" + hexAddr,
	}}
	got, err := resolveTarStrkey("", refs, 42)
	require.NoError(t, err)
	require.Len(t, got, 56) // C… strkey form
	// An explicit override wins and must be a valid strkey.
	_, err = resolveTarStrkey(got, nil, 42)
	require.NoError(t, err)
	_, err = resolveTarStrkey("", refs, 43)
	require.ErrorContains(t, err, "no TokenAdminRegistry ref found")
	_, err = resolveTarStrkey("nope", nil, 42)
	require.ErrorContains(t, err, "invalid registry strkey")
}

package accessors

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/keystore"

	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

// stubNetResult is one scripted GetNetwork outcome; the last result repeats.
type stubNetResult struct {
	passphrase string
	err        error
}

type stubNetworkGetter struct {
	results []stubNetResult
	calls   int
}

func (s *stubNetworkGetter) GetNetwork(_ context.Context) (protocolrpc.GetNetworkResponse, error) {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	r := s.results[i]
	if r.err != nil {
		return protocolrpc.GetNetworkResponse{}, r.err
	}
	return protocolrpc.GetNetworkResponse{Passphrase: r.passphrase}, nil
}

func TestVerifyNetworkIdentity_Match(t *testing.T) {
	chain := chainsel.STELLAR_TESTNET
	client := &stubNetworkGetter{results: []stubNetResult{{passphrase: chain.Passphrase}}}

	err := verifyNetworkIdentity(context.Background(), client, chain.Passphrase, protocol.ChainSelector(chain.Selector), zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, 1, client.calls, "a definitive answer needs no retries")
}

func TestVerifyNetworkIdentity_PassphraseMismatch(t *testing.T) {
	// URL/passphrase disagreement: endpoint serves mainnet, config signs for
	// testnet. The reader path would never notice (reads do not use the
	// passphrase); the transmitter path would fail per-transaction.
	chain := chainsel.STELLAR_TESTNET
	client := &stubNetworkGetter{results: []stubNetResult{{passphrase: chainsel.STELLAR_MAINNET.Passphrase}}}

	err := verifyNetworkIdentity(context.Background(), client, chain.Passphrase, protocol.ChainSelector(chain.Selector), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), chainsel.STELLAR_MAINNET.Passphrase)
	assert.Contains(t, err.Error(), chain.Passphrase)
	assert.Equal(t, 1, client.calls, "mismatch is definitive, not retried")
}

func TestVerifyNetworkIdentity_SelectorMismatch(t *testing.T) {
	// URL and passphrase wrong together: endpoint and config consistently
	// describe mainnet while the chain selector refers to testnet. The
	// passphrase check alone cannot catch this; the network-id-vs-selector
	// comparison must.
	client := &stubNetworkGetter{results: []stubNetResult{{passphrase: chainsel.STELLAR_MAINNET.Passphrase}}}

	err := verifyNetworkIdentity(context.Background(), client, chainsel.STELLAR_MAINNET.Passphrase, protocol.ChainSelector(chainsel.STELLAR_TESTNET.Selector), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), chainsel.STELLAR_MAINNET.ChainID)
	assert.Contains(t, err.Error(), chainsel.STELLAR_TESTNET.ChainID)
}

func TestVerifyNetworkIdentity_UnknownSelectorSkipsSelectorCheck(t *testing.T) {
	// A selector chain-selectors does not know (custom network): only the
	// configured-passphrase invariant can be verified.
	const unknownSelector = 12345
	client := &stubNetworkGetter{results: []stubNetResult{{passphrase: chainsel.STELLAR_TESTNET.Passphrase}}}

	err := verifyNetworkIdentity(context.Background(), client, chainsel.STELLAR_TESTNET.Passphrase, protocol.ChainSelector(unknownSelector), zerolog.Nop())
	require.NoError(t, err)
}

func TestVerifyNetworkIdentity_UnreachableRetriesThenSkips(t *testing.T) {
	prev := networkVerifyBackoff
	networkVerifyBackoff = time.Millisecond
	t.Cleanup(func() { networkVerifyBackoff = prev })

	client := &stubNetworkGetter{results: []stubNetResult{{err: errors.New("connection refused")}}}

	// An unreachable endpoint must not brick the accessor: SetKeystore runs
	// once at bootstrap, so connectivity loss skips the check rather than
	// permanently disabling the lane.
	err := verifyNetworkIdentity(context.Background(), client, chainsel.STELLAR_TESTNET.Passphrase, protocol.ChainSelector(chainsel.STELLAR_TESTNET.Selector), zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, networkVerifyAttempts, client.calls)
}

func TestVerifyNetworkIdentity_TransientFailureThenSuccess(t *testing.T) {
	prev := networkVerifyBackoff
	networkVerifyBackoff = time.Millisecond
	t.Cleanup(func() { networkVerifyBackoff = prev })

	chain := chainsel.STELLAR_TESTNET
	client := &stubNetworkGetter{results: []stubNetResult{
		{err: errors.New("connection reset")},
		{passphrase: chain.Passphrase},
	}}

	err := verifyNetworkIdentity(context.Background(), client, chain.Passphrase, protocol.ChainSelector(chain.Selector), zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, 2, client.calls)
}

func TestAccessor_SetKeystore_NetworkMismatchFailsGetters(t *testing.T) {
	ctx := context.Background()
	f, _ := newDestFactoryUnderTest(t)
	f.networkVerify = func(context.Context, sourcereader.ReaderConfig, protocol.ChainSelector, zerolog.Logger) error {
		return errors.New("endpoint network passphrase \"x\" does not match configured passphrase \"y\"")
	}
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc := accIface.(*accessor)

	ks := newTestKeystore(t)
	createKey(t, ks, "stellar/tx/dest-test", keystore.Ed25519)
	require.NoError(t, acc.SetKeystore(ctx, ks))

	// A definitive network mismatch is immutable configuration: every
	// keystore-backed getter reports the same root cause.
	_, err = acc.SourceReader()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network identity check")
	_, err = acc.DestinationReader()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network identity check")
	_, err = acc.ContractTransmitter()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network identity check")
}

func TestAccessor_SetKeystore_NetworkVerifyPassesBuildsComponents(t *testing.T) {
	ctx := context.Background()
	f, _ := newDestFactoryUnderTest(t)
	var verifiedWith sourcereader.ReaderConfig
	var verifiedSelector protocol.ChainSelector
	f.networkVerify = func(_ context.Context, cfg sourcereader.ReaderConfig, sel protocol.ChainSelector, _ zerolog.Logger) error {
		verifiedWith = cfg
		verifiedSelector = sel
		return nil
	}
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc := accIface.(*accessor)

	ks := newTestKeystore(t)
	createKey(t, ks, "stellar/tx/dest-test", keystore.Ed25519)
	require.NoError(t, acc.SetKeystore(ctx, ks))

	// The check saw the configured URL/passphrase and chain selector.
	assert.Equal(t, f.readerConfig[strconv.FormatUint(stellarSelector, 10)], verifiedWith)
	assert.Equal(t, protocol.ChainSelector(stellarSelector), verifiedSelector)

	// With identity verified, all components build normally.
	dr, err := acc.DestinationReader()
	require.NoError(t, err)
	require.NotNil(t, dr)
	ct, err := acc.ContractTransmitter()
	require.NoError(t, err)
	require.NotNil(t, ct)
	sr, err := acc.SourceReader()
	require.NoError(t, err)
	require.NotNil(t, sr)
}

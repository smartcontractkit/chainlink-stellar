package accessors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/bootstrap"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/keystore"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	contracttransmitter "github.com/smartcontractkit/chainlink-stellar/ccv/contract_transmitter"
	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// destinationConfig holds the resolved per-chain settings the accessor needs to
// build executor-side components (DestinationReader + ContractTransmitter)
// once a keystore is injected. Reader-side fields live on accessor.readerCfg.
type destinationConfig struct {
	// offRampContractID is the OffRamp contract address in Stellar strkey form.
	offRampContractID string
	// rmnRemoteContractID is the RMN Remote contract address in Stellar strkey form.
	rmnRemoteContractID string
	// stateChangedTopic is the offramp event topic the ContractTransmitter watches.
	stateChangedTopic string
	// keyName is the name of the Ed25519 key in the bootstrap keystore that the
	// destination-side Deployer uses to sign Soroban transactions.
	keyName string
}

type factory struct {
	lggr logger.Logger

	// readerConfig is keyed by chain selector (decimal string) and holds
	// SourceReader settings (Soroban RPC URL, network passphrase, OnRamp /
	// RMN-Remote contract IDs).
	readerConfig map[string]sourcereader.ReaderConfig

	// destConfig is keyed by chain selector (decimal string) and holds the
	// settings needed to build executor-side components for that chain. May be
	// nil for committee-verifier-only deployments where no executor config was
	// provided in the GenericConfig overlay.
	destConfig map[string]destinationConfig

	// attemptCacheExpiration mirrors executor.Configuration.MaxRetryDuration and
	// is forwarded to destinationreader.New so the execution-attempt poller's
	// look-back window matches the executor's retry budget.
	attemptCacheExpiration time.Duration
}

// GetAccessor implements chainaccess.AccessorFactory.
//
// GetAccessor returns a valid Accessor whenever the chain selector is recognized,
// even if one or more capabilities (e.g. SourceReader when on_ramp_addresses is
// absent, or ContractTransmitter when no keystore has been injected yet) cannot
// be constructed at this time. Missing capabilities are reported as errors only
// when the corresponding getter is called.
func (f *factory) GetAccessor(ctx context.Context, chainSelector protocol.ChainSelector) (chainaccess.Accessor, error) {
	if f.readerConfig == nil && f.destConfig == nil {
		return nil, fmt.Errorf("stellar ccip config is not set - can't get accessor for chain %d", chainSelector)
	}

	family, err := chainsel.GetSelectorFamily(uint64(chainSelector))
	if err != nil {
		return nil, fmt.Errorf("failed to get selector family for %d - update chain-selectors library?: %w", chainSelector, err)
	}
	if family != chainsel.FamilyStellar {
		return nil, fmt.Errorf("skipping chain, only stellar is supported for chain %d, family %s", chainSelector, family)
	}

	strSelector := strconv.FormatUint(uint64(chainSelector), 10)
	stellarReaderConfig, hasReader := f.readerConfig[strSelector]
	stellarDestConfig, hasDest := f.destConfig[strSelector]
	if !hasReader && !hasDest {
		return nil, fmt.Errorf("stellar config not found for chain %d", chainSelector)
	}

	zerologLogger := zerolog.New(os.Stdout).
		With().
		Str("chain_selector", strSelector).
		Logger().
		Level(zerolog.InfoLevel)

	a := &accessor{
		lggr:                   f.lggr,
		zlggr:                  zerologLogger,
		chainSelector:          chainSelector,
		strSelector:            strSelector,
		readerCfg:              stellarReaderConfig,
		hasReader:              hasReader,
		destCfg:                stellarDestConfig,
		hasDest:                hasDest,
		attemptCacheExpiration: f.attemptCacheExpiration,
	}

	// Pre-validate reader-side config now so SourceReader() can return a
	// meaningful error even before SetKeystore is invoked. The actual
	// SourceReader is built lazily once the keystore is available, since we
	// want the deployer account that signs (read-only) RPC simulations to be
	// the same Ed25519 key that signs writes.
	if hasReader {
		if stellarReaderConfig.SorobanRPCURL == "" {
			a.sourceReaderErr = fmt.Errorf("soroban rpc url is required for chain %d", chainSelector)
		} else if stellarReaderConfig.NetworkPassphrase == "" {
			a.sourceReaderErr = fmt.Errorf("network passphrase is required for chain %d", chainSelector)
		}
	} else {
		a.sourceReaderErr = errReaderConfigMissing
	}

	if !hasDest {
		a.destReaderErr = errDestConfigMissing
		a.contractTransmitterErr = errDestConfigMissing
	}

	return a, nil
}

// NewFactory builds a Stellar AccessorFactory from already-merged reader and
// destination config maps. Callers that want the bootstrap-driven config flow
// should use CreateStellarAccessorFactory; this constructor is exposed for
// tests and for the few unit-test helpers that need a hand-crafted factory.
func NewFactory(
	lggr logger.Logger,
	readerConfig map[string]sourcereader.ReaderConfig,
	destConfig map[string]destinationConfig,
	attemptCacheExpiration time.Duration,
) chainaccess.AccessorFactory {
	return &factory{
		lggr:                   lggr,
		readerConfig:           readerConfig,
		destConfig:             destConfig,
		attemptCacheExpiration: attemptCacheExpiration,
	}
}

// NewReaderFactory builds a Stellar AccessorFactory that only serves the
// committee-verifier (source-reader) path, leaving DestinationReader and
// ContractTransmitter as not-implemented errors. This preserves backwards
// compatibility with existing tests that called NewFactory(lggr, configMap)
// before the executor path was added.
func NewReaderFactory(lggr logger.Logger, readerConfig map[string]sourcereader.ReaderConfig) chainaccess.AccessorFactory {
	return NewFactory(lggr, readerConfig, nil, 0)
}

type accessor struct {
	lggr  logger.Logger
	zlggr zerolog.Logger

	chainSelector          protocol.ChainSelector
	strSelector            string
	readerCfg              sourcereader.ReaderConfig
	hasReader              bool
	destCfg                destinationConfig
	hasDest                bool
	attemptCacheExpiration time.Duration

	// mu guards the lazy-built fields below; SetKeystore may be invoked
	// concurrently with reader / transmitter accessors in pathological cases.
	mu sync.Mutex

	sourceReader           chainaccess.SourceReader
	sourceReaderErr        error
	destinationReader      chainaccess.DestinationReader
	destReaderErr          error
	contractTransmitter    chainaccess.ContractTransmitter
	contractTransmitterErr error
}

var (
	errReaderConfigMissing = errors.New("stellar reader config not present for this chain (committee/verifier path disabled)")
	errDestConfigMissing   = errors.New("stellar destination config not present for this chain (executor path disabled)")
	errKeystoreNotInjected = errors.New("stellar accessor requires SetKeystore to be called before constructing keystore-backed components")
)

// Compile-time guarantee that the accessor satisfies bootstrap.KeystoreSetter;
// without it a signature drift degrades to "keystore not injected" warnings at
// runtime instead of a build error.
var _ bootstrap.KeystoreSetter = (*accessor)(nil)

// SetKeystore implements bootstrap.KeystoreSetter. It is invoked automatically
// by bootstrap.KeystoreRegistry after every GetAccessor call so the accessor
// can build keystore-backed components without holding raw key material.
//
// Behaviour:
//   - When ks is nil the call is a no-op + warning, matching the EVM accessor.
//   - When the destination key name is empty (committee-verifier deployments)
//     we still build the SourceReader using the keystore-backed deployer, so
//     even read-only Soroban simulations use a deterministic account.
//   - Errors during construction are stored in *Err fields so the failing
//     getter (DestinationReader / ContractTransmitter / SourceReader) returns
//     the original cause, while unaffected getters keep working. We return nil
//     rather than the error so a keystore problem on one chain does not abort
//     accessor construction for the rest of the job's chains.
func (a *accessor) SetKeystore(ctx context.Context, ks keystore.Keystore) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ks == nil {
		a.zlggr.Warn().
			Str("chain_selector", a.strSelector).
			Msg("stellar accessor: SetKeystore called with nil keystore; keystore-backed components will not be constructed")
		return nil
	}

	keyName := a.destCfg.keyName
	if keyName == "" {
		// committee-verifier-only deployments (no executor key declared) still
		// benefit from a deterministic Stellar source-reader account.
		keyName = common.StellarTransmitterKeyName
	}

	signer, err := LoadStellarKeystoreSigner(ctx, ks, keyName)
	if err != nil {
		a.zlggr.Error().
			Err(err).
			Str("key_name", keyName).
			Msg("stellar accessor: failed to load keystore signer")
		// Surface the keystore error on every keystore-backed getter so callers
		// see the same root cause regardless of which component they request.
		if a.sourceReaderErr == nil && a.hasReader {
			a.sourceReaderErr = fmt.Errorf("load stellar keystore signer: %w", err)
		}
		if a.hasDest {
			a.destReaderErr = fmt.Errorf("load stellar keystore signer: %w", err)
			a.contractTransmitterErr = fmt.Errorf("load stellar keystore signer: %w", err)
		}
		return nil
	}
	a.zlggr.Info().
		Str("key_name", keyName).
		Str("address", signer.Address()).
		Msg("stellar accessor: keystore signer ready")

	if a.hasReader && a.sourceReaderErr == nil {
		sr, err := buildSourceReaderWithSigner(a.readerCfg, signer, a.zlggr)
		if err != nil {
			a.sourceReaderErr = err
		} else {
			a.sourceReader = sr
		}
	}

	if a.hasDest {
		dr, ct, err := buildDestinationComponents(a.destCfg, a.readerCfg, signer, a.attemptCacheExpiration, a.zlggr)
		if err != nil {
			a.destReaderErr = err
			a.contractTransmitterErr = err
			return nil
		}
		a.destinationReader = dr
		a.contractTransmitter = ct
		// Successful construction clears any "missing" error left over from
		// GetAccessor when the destination config was present but the keystore
		// hadn't arrived yet.
		a.destReaderErr = nil
		a.contractTransmitterErr = nil
	}
	return nil
}

func (a *accessor) SourceReader() (chainaccess.SourceReader, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sourceReaderErr != nil {
		return nil, a.sourceReaderErr
	}
	if a.sourceReader == nil {
		return nil, errKeystoreNotInjected
	}
	return a.sourceReader, nil
}

func (a *accessor) DestinationReader() (chainaccess.DestinationReader, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.destReaderErr != nil {
		return nil, a.destReaderErr
	}
	if a.destinationReader == nil {
		return nil, errKeystoreNotInjected
	}
	return a.destinationReader, nil
}

func (a *accessor) ContractTransmitter() (chainaccess.ContractTransmitter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.contractTransmitterErr != nil {
		return nil, a.contractTransmitterErr
	}
	if a.contractTransmitter == nil {
		return nil, errKeystoreNotInjected
	}
	return a.contractTransmitter, nil
}

// Close implements chainaccess.Accessor. The Stellar accessor is stateless
// (no background services), so this is a no-op that returns nil.
func (a *accessor) Close() error {
	return nil
}

// buildSourceReaderWithSigner constructs a SourceReader using a keystore-backed
// TxSigner for the underlying Stellar Deployer (Soroban Invoker). The
// Soroban-side calls the SourceReader makes are read-only simulations, but
// using the keystore signer keeps the deployer account stable across restarts
// and matches the executor-side wiring on the same binary.
func buildSourceReaderWithSigner(
	cfg sourcereader.ReaderConfig,
	signer deployment.TxSigner,
	zlggr zerolog.Logger,
) (chainaccess.SourceReader, error) {
	rpcClient := rpcclient.NewClient(cfg.SorobanRPCURL, &http.Client{Timeout: 60 * time.Second})
	deployer := deployment.NewDeployerWithSigner(rpcClient, cfg.NetworkPassphrase, signer)

	sr, err := sourcereader.NewSourceReaderWithClient(
		rpcClient,
		deployer,
		cfg.OnRampContractID,
		common.StellarCCIPMessageSentTopic,
		cfg.RMNRemoteContractID,
		&zlggr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create source reader: %w", err)
	}
	return sr, nil
}

// buildDestinationComponents constructs both DestinationReader and
// ContractTransmitter for the executor path. Both share the same RPC client
// and Deployer instance so they hit the same node and submit transactions
// from the same Stellar account.
func buildDestinationComponents(
	dest destinationConfig,
	reader sourcereader.ReaderConfig,
	signer deployment.TxSigner,
	attemptCacheExpiration time.Duration,
	zlggr zerolog.Logger,
) (chainaccess.DestinationReader, chainaccess.ContractTransmitter, error) {
	if dest.offRampContractID == "" {
		return nil, nil, fmt.Errorf("offramp contract id is required")
	}
	if reader.SorobanRPCURL == "" {
		return nil, nil, fmt.Errorf("soroban rpc url is required (set reader_configs[%q].soroban_rpc_url)", "selector")
	}
	if reader.NetworkPassphrase == "" {
		return nil, nil, fmt.Errorf("network passphrase is required (set reader_configs[%q].network_passphrase)", "selector")
	}

	rpcClient := rpcclient.NewClient(reader.SorobanRPCURL, &http.Client{Timeout: 60 * time.Second})
	deployer := deployment.NewDeployerWithSigner(rpcClient, reader.NetworkPassphrase, signer)

	stateChangedTopic := dest.stateChangedTopic
	if stateChangedTopic == "" {
		// Fall back to the same topic used by the SourceReader on the source
		// side for its own state-changed events; in the executor path this is
		// rarely empty because TransmitterConfig populates it from the Stellar
		// TOML, but guard anyway so we don't pass "" to NewContractTransmitter.
		return nil, nil, fmt.Errorf("ccip state changed topic is required (set transmitter_configs[%q].ccip_state_changed_topic)", "selector")
	}

	ct, err := contracttransmitter.NewContractTransmitterWithClient(
		deployer,
		dest.offRampContractID,
		stateChangedTopic,
		dest.rmnRemoteContractID,
		&zlggr,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create contract transmitter: %w", err)
	}

	dr, err := destinationreader.New(
		deployer,
		rpcClient,
		dest.offRampContractID,
		dest.rmnRemoteContractID,
		&zlggr,
		attemptCacheExpiration,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create destination reader: %w", err)
	}

	return dr, ct, nil
}

package accessors

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

type factory struct {
	lggr logger.Logger

	// map of chain selector to Stellar reader config
	config map[string]sourcereader.ReaderConfig
}

// GetAccessor implements chainaccess.AccessorFactory.
func (f *factory) GetAccessor(ctx context.Context, chainSelector protocol.ChainSelector) (chainaccess.Accessor, error) {
	if f.config == nil {
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
	stellarConfig, ok := f.config[strSelector]
	if !ok {
		return nil, fmt.Errorf("stellar config not found for chain %d", chainSelector)
	}

	// TODO: move it into its own method separately
	if stellarConfig.SorobanRPCURL == "" {
		return nil, fmt.Errorf("soroban rpc url is required for chain %d", chainSelector)
	}
	if stellarConfig.NetworkPassphrase == "" {
		return nil, fmt.Errorf("network passphrase is required for chain %d", chainSelector)
	}

	// TODO: get the deployer's keypair from env instead of generating a random one
	deployerKeypair, err := keypair.Random()
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer keypair: %w", err)
	}

	zerologLogger := zerolog.New(os.Stdout).With().Str("chain_selector", strSelector).Logger().Level(zerolog.DebugLevel)

	rpcRaw := rpcclient.NewClient(stellarConfig.SorobanRPCURL, &http.Client{Timeout: 60 * time.Second})
	client := ccvclient.NewClient(rpcRaw)
	deployer := deployment.NewDeployer(
		rpcRaw,
		stellarConfig.NetworkPassphrase,
		deployerKeypair,
	)

	sourceReader, err := sourcereader.NewSourceReaderWithClient(
		client.RPC,
		deployer,
		stellarConfig.OnRampContractID,
		common.StellarCCIPMessageSentTopic,
		stellarConfig.RMNRemoteContractID,
		&zerologLogger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create source reader: %w", err)
	}

	return newAccessor(sourceReader), nil
}

func NewFactory(lggr logger.Logger, config map[string]sourcereader.ReaderConfig) chainaccess.AccessorFactory {
	return &factory{
		lggr:   lggr,
		config: config,
	}
}

type accessor struct {
	sourceReader chainaccess.SourceReader
}

func newAccessor(sourceReader chainaccess.SourceReader) chainaccess.Accessor {
	return &accessor{
		sourceReader: sourceReader,
	}
}

func (a *accessor) SourceReader() chainaccess.SourceReader {
	return a.sourceReader
}

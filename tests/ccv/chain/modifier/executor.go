package modifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/rs/zerolog"
	"github.com/testcontainers/testcontainers-go"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/bootstrap"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/executor"
	executorpkg "github.com/smartcontractkit/chainlink-ccv/executor"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	contracttransmitter "github.com/smartcontractkit/chainlink-stellar/ccv/contract_transmitter"
	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

const defaultStellarExecutorImage = "stellarexecutor:dev"

// StellarExecutorModifier is an executor.ReqModifier that configures the container
// request for the Stellar executor:
//  1. Switches the image to stellarexecutor:dev.
//  2. Builds a stellar.toml from the executor's GeneratedConfig and Stellar network
//     info (passphrase + internal RPC URL from blockchain outputs), including
//     TransmitterConfigs, DestinationReaderConfigs, and ReaderConfigs, then
//     bind-mounts it at DefaultStellarConfigPath so the binary reads it on startup.
func StellarExecutorModifier(req testcontainers.ContainerRequest, executorInput *executor.Input, outputs []*blockchain.Output) (testcontainers.ContainerRequest, error) {
	req.Image = defaultStellarExecutorImage
	req.Name = fmt.Sprintf("stellar-%s", executorInput.ContainerName)

	configBytes, err := buildExecutorStellarConfig(executorInput, outputs)
	if err != nil {
		return req, fmt.Errorf("building stellar config for %s: %w", executorInput.ContainerName, err)
	}

	configFilePath := filepath.Join(
		os.TempDir(),
		fmt.Sprintf("stellar-executor-%s-config.toml", executorInput.ContainerName),
	)
	if err := os.WriteFile(configFilePath, configBytes, 0o644); err != nil {
		return req, fmt.Errorf("writing stellar config for %s: %w", executorInput.ContainerName, err)
	}

	//nolint:staticcheck
	req.Mounts = append(req.Mounts, testcontainers.BindMount(configFilePath, common.DefaultStellarConfigPath))

	return req, nil
}

// buildExecutorStellarConfig constructs a common.Config with TransmitterConfigs,
// DestinationReaderConfigs, and ReaderConfigs, then serialises it as TOML.
//
// The chainlink-ccv executor moved from a single GeneratedConfig string to a
// list of GeneratedJobSpecs (each carrying its own AppConfig); see
// chainlink-ccv changelog/2026-04-27_executor_jd_migration.md and the May 1
// keystore-transmitter change. We parse the first job spec's AppConfig as the
// executor configuration and pull per-chain OffRamp / RMN-Remote addresses
// from there.
func buildExecutorStellarConfig(executorInput *executor.Input, outputs []*blockchain.Output) ([]byte, error) {
	var executorCfg executorpkg.Configuration
	if len(executorInput.GeneratedJobSpecs) > 0 {
		var spec bootstrap.JobSpec
		if _, err := toml.Decode(executorInput.GeneratedJobSpecs[0], &spec); err != nil {
			return nil, fmt.Errorf("parse generated job spec: %w", err)
		}
		if spec.AppConfig != "" {
			if _, err := toml.Decode(spec.AppConfig, &executorCfg); err != nil {
				return nil, fmt.Errorf("parse executor app config: %w", err)
			}
		}
	}

	l := zerolog.New(os.Stderr).Level(zerolog.DebugLevel).With().Fields(map[string]any{"component": "stellar-executor-modifier"}).Logger()
	l.Info().Msgf("Executor config: %+v", executorCfg)

	readerConfigs := make(map[string]sourcereader.ReaderConfig)
	transmitterConfigs := make(map[string]contracttransmitter.ContractTransmitterConfig)
	destReaderConfigs := make(map[string]destinationreader.Config)

	for _, output := range outputs {
		if output.Family != chainsel.FamilyStellar {
			continue
		}

		details, err := chainsel.GetChainDetailsByChainIDAndFamily(output.ChainID, output.Family)
		if err != nil {
			return nil, fmt.Errorf("get chain details for Stellar chain %s: %w", output.ChainID, err)
		}
		strSelector := strconv.FormatUint(details.ChainSelector, 10)

		if output.NetworkSpecificData == nil || output.NetworkSpecificData.StellarNetwork == nil {
			return nil, fmt.Errorf("missing Stellar network info in output for chain %s", output.ChainID)
		}
		networkPassphrase := output.NetworkSpecificData.StellarNetwork.NetworkPassphrase

		if len(output.Nodes) == 0 {
			return nil, fmt.Errorf("no nodes in output for Stellar chain %s", output.ChainID)
		}
		sorobanRPCURL := output.Nodes[0].InternalHTTPUrl

		// Resolve OffRamp and RMN Remote addresses from executor ChainConfiguration
		var offRampContractID, rmnRemoteContractID string
		if chainCfg, ok := executorCfg.ChainConfiguration[strSelector]; ok {
			if chainCfg.OffRampAddress != "" {
				offRampContractID, err = scval.HexToContractStrkey(chainCfg.OffRampAddress)
				if err != nil {
					return nil, fmt.Errorf("convert OffRamp hex to strkey for chain %s: %w", strSelector, err)
				}
			}
			if chainCfg.RmnAddress != "" {
				rmnRemoteContractID, err = scval.HexToContractStrkey(chainCfg.RmnAddress)
				if err != nil {
					return nil, fmt.Errorf("convert RMN Remote hex to strkey for chain %s: %w", strSelector, err)
				}
			}
		}

		readerConfigs[strSelector] = sourcereader.ReaderConfig{
			NetworkPassphrase:   networkPassphrase,
			SorobanRPCURL:       sorobanRPCURL,
			RMNRemoteContractID: rmnRemoteContractID,
		}

		transmitterConfigs[strSelector] = contracttransmitter.ContractTransmitterConfig{
			NetworkPassphrase:     networkPassphrase,
			OffRampContractID:     offRampContractID,
			CCIPOfframpAddress:    offRampContractID,
			CCIPStateChangedTopic: "offramp_1_7_ExecStateChanged",
			RMNRemoteAddress:      rmnRemoteContractID,
		}

		destReaderConfigs[strSelector] = destinationreader.Config{
			OffRampContractID:   offRampContractID,
			RMNRemoteContractID: rmnRemoteContractID,
		}
	}

	cfg := common.Config{
		ReaderConfigs:            readerConfigs,
		TransmitterConfigs:       transmitterConfigs,
		DestinationReaderConfigs: destReaderConfigs,
	}
	configBytes, err := toml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal stellar config: %w", err)
	}

	return configBytes, nil
}

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
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"
	"github.com/smartcontractkit/chainlink-ccv/verifier/pkg/commit"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

const defaultStellarVerifierImage = "stellarcommittee-verifier:dev"

// StellarVerifierModifier is a committeeverifier.ReqModifier that configures the container
// request for the Stellar committee verifier:
//  1. Switches the image from the default EVM verifier to stellarcommittee-verifier:dev.
//  2. Builds a stellar.toml from real deployed addresses (via verifierInput.GeneratedConfig)
//     and Stellar network info (passphrase + internal RPC URL from blockchain outputs), then
//     bind-mounts it at DefaultStellarConfigPath so the binary reads it on startup.
func StellarVerifierModifier(req testcontainers.ContainerRequest, verifierInput *committeeverifier.Input, outputs []*blockchain.Output) (testcontainers.ContainerRequest, error) {
	req.Image = defaultStellarVerifierImage
	req.Name = fmt.Sprintf("stellar-%s", verifierInput.ContainerName)

	configBytes, err := buildVerifierStellarConfig(verifierInput, outputs)
	if err != nil {
		return req, fmt.Errorf("building stellar config for %s: %w", verifierInput.ContainerName, err)
	}

	configFilePath := filepath.Join(
		os.TempDir(),
		fmt.Sprintf("stellar-%s-config-%d.toml", verifierInput.CommitteeName, verifierInput.NodeIndex+1),
	)
	if err := os.WriteFile(configFilePath, configBytes, 0o644); err != nil {
		return req, fmt.Errorf("writing stellar config for %s: %w", verifierInput.ContainerName, err)
	}

	//nolint:staticcheck
	req.Mounts = append(req.Mounts, testcontainers.BindMount(configFilePath, common.DefaultStellarConfigPath))

	return req, nil
}

// buildVerifierStellarConfig constructs a common.Config with ReaderConfigs and serialises it as TOML.
func buildVerifierStellarConfig(verifierInput *committeeverifier.Input, outputs []*blockchain.Output) ([]byte, error) {
	var deployedCfg commit.Config
	if verifierInput.GeneratedConfig != "" {
		if _, err := toml.Decode(verifierInput.GeneratedConfig, &deployedCfg); err != nil {
			return nil, fmt.Errorf("parse GeneratedConfig: %w", err)
		}
	}

	l := zerolog.New(os.Stderr).Level(zerolog.DebugLevel).With().Fields(map[string]any{"component": "stellar-verifier-modifier"}).Logger()
	l.Info().Msgf("Deployed config: %+v", deployedCfg)

	readerConfigs := make(map[string]sourcereader.ReaderConfig)

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

		// Contract addresses may not be available yet: the modifier runs
		// when verifiers are launched early (before contract deployment)
		// so that signing keys can be discovered. The verifier binary
		// back-fills missing addresses from the job-spec commit.Config
		// which IS generated after deployment.
		var onrampContractID string
		if onrampHex, ok := deployedCfg.OnRampAddresses[strSelector]; ok && onrampHex != "" {
			onrampContractID, err = scval.HexToContractStrkey(onrampHex)
			if err != nil {
				return nil, fmt.Errorf("convert OnRamp hex to strkey for chain %s: %w", strSelector, err)
			}
		}

		var rmnRemoteContractID string
		if rmnRemoteHex, ok := deployedCfg.RMNRemoteAddresses[strSelector]; ok && rmnRemoteHex != "" {
			rmnRemoteContractID, err = scval.HexToContractStrkey(rmnRemoteHex)
			if err != nil {
				return nil, fmt.Errorf("convert RMN Remote hex to strkey for chain %s: %w", strSelector, err)
			}
		}

		readerConfigs[strSelector] = sourcereader.ReaderConfig{
			NetworkPassphrase:   networkPassphrase,
			SorobanRPCURL:       sorobanRPCURL,
			OnRampContractID:    onrampContractID,
			RMNRemoteContractID: rmnRemoteContractID,
		}
	}

	cfg := common.Config{ReaderConfigs: readerConfigs}
	configBytes, err := toml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal stellar config: %w", err)
	}

	return configBytes, nil
}

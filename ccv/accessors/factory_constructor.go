package accessors

import (
	"fmt"
	"maps"
	"os"

	"github.com/BurntSushi/toml"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

func init() {
	chainaccess.Register(chainsel.FamilyStellar, CreateStellarAccessorFactory)
}

var _ chainaccess.AccessorFactoryConstructor = CreateStellarAccessorFactory

// StellarConfigPathEnv is the env var for the bind-mounted Stellar TOML (RPC, etc.).
const StellarConfigPathEnv = "STELLAR_CONFIG_PATH"

func loadStellarFileConfig(path string) (*common.Config, error) {
	var cfg common.Config
	if md, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w", path, err)
	} else if len(md.Undecoded()) > 0 {
		return nil, fmt.Errorf("unknown fields in config: %v", md.Undecoded())
	}
	return &cfg, nil
}

// stellarConfigPath returns STELLAR_CONFIG_PATH or the default bind-mount path.
func stellarConfigPath() string {
	if p, ok := os.LookupEnv(StellarConfigPathEnv); ok {
		return p
	}
	return common.DefaultStellarConfigPath
}

// applyOnRampRMNHexOverrides fills empty OnRampContractID / RMNRemoteContractID from
// committee-style hex maps (job spec), converting to Stellar contract strkeys.
func applyOnRampRMNHexOverrides(
	readerConfigs map[string]sourcereader.ReaderConfig,
	onRampHexBySelector map[string]string,
	rmnRemoteHexBySelector map[string]string,
) error {
	for sel, rc := range readerConfigs {
		if rc.OnRampContractID == "" {
			if onrampHex, ok := onRampHexBySelector[sel]; ok && onrampHex != "" {
				addr, err := scval.HexToContractStrkey(onrampHex)
				if err != nil {
					return fmt.Errorf("convert OnRamp hex to strkey for chain %s: %w", sel, err)
				}
				rc.OnRampContractID = addr
			}
		}
		if rc.RMNRemoteContractID == "" {
			if rmnHex, ok := rmnRemoteHexBySelector[sel]; ok && rmnHex != "" {
				addr, err := scval.HexToContractStrkey(rmnHex)
				if err != nil {
					return fmt.Errorf("convert RMN Remote hex to strkey for chain %s: %w", sel, err)
				}
				rc.RMNRemoteContractID = addr
			}
		}
		readerConfigs[sel] = rc
	}
	return nil
}

// buildStellarDestConfigs builds the per-chain destinationConfig map used by
// the executor path. It draws OffRamp and RMN remote contract IDs from (in
// order of preference):
//
//  1. The Stellar TOML file's destination_reader_configs / transmitter_configs
//     entries (existing semantics from cmd/executor/bootstrap.go).
//  2. genericConfig.ChainConfiguration[selector].OffRampAddress / RmnAddress
//     hex addresses, converted to Stellar strkeys via scval.HexToContractStrkey.
//
// CCIPStateChangedTopic is sourced from the Stellar TOML's transmitter_configs;
// it must be present for the executor path to be enabled.
//
// Returns an empty map (nil) when no Stellar destination configuration is
// available, in which case the verifier path still works.
func buildStellarDestConfigs(
	stellarFileCfg *common.Config,
	genericConfig chainaccess.GenericConfig,
) (map[string]destinationConfig, error) {
	out := make(map[string]destinationConfig)

	// Seed from Stellar TOML transmitter_configs / destination_reader_configs.
	for sel, tc := range stellarFileCfg.TransmitterConfigs {
		out[sel] = destinationConfig{
			offRampContractID:   tc.OffRampContractID,
			rmnRemoteContractID: tc.RMNRemoteAddress,
			stateChangedTopic:   tc.CCIPStateChangedTopic,
			keyName:             common.StellarTransmitterKeyName,
		}
	}
	for sel, drCfg := range stellarFileCfg.DestinationReaderConfigs {
		dc := out[sel]
		if dc.offRampContractID == "" && drCfg.OffRampContractID != "" {
			dc.offRampContractID = drCfg.OffRampContractID
		}
		if dc.rmnRemoteContractID == "" && drCfg.RMNRemoteContractID != "" {
			dc.rmnRemoteContractID = drCfg.RMNRemoteContractID
		}
		if dc.keyName == "" {
			dc.keyName = common.StellarTransmitterKeyName
		}
		out[sel] = dc
	}

	// Overlay hex addresses from the executor ChainConfiguration. The
	// bind-mounted Stellar TOML is generated before contracts are deployed so
	// OffRamp / RMN-Remote IDs may be missing; the executor service config
	// (assembled post-deployment) carries them as hex strings.
	for sel, chainCfg := range genericConfig.ChainConfiguration {
		dc := out[sel]
		if dc.offRampContractID == "" && chainCfg.OffRampAddress != "" {
			addr, err := scval.HexToContractStrkey(chainCfg.OffRampAddress)
			if err != nil {
				return nil, fmt.Errorf("convert OffRamp hex to strkey for chain %s: %w", sel, err)
			}
			dc.offRampContractID = addr
		}
		if dc.rmnRemoteContractID == "" && chainCfg.RmnAddress != "" {
			addr, err := scval.HexToContractStrkey(chainCfg.RmnAddress)
			if err != nil {
				return nil, fmt.Errorf("convert RMN Remote hex to strkey for chain %s: %w", sel, err)
			}
			dc.rmnRemoteContractID = addr
		}
		if dc.keyName == "" {
			dc.keyName = common.StellarTransmitterKeyName
		}
		// chainaccess.DestinationChainConfig.TransmitterKeyName lets operators
		// override the Stellar transmitter key per chain, mirroring the EVM
		// accessor's behaviour.
		if chainCfg.TransmitterKeyName != "" {
			dc.keyName = chainCfg.TransmitterKeyName
		}
		out[sel] = dc
	}

	// Drop entries that did not yield a usable executor configuration so the
	// accessor doesn't claim destination capabilities for chains that lack the
	// data (e.g. a verifier-only deployment that listed every Stellar chain in
	// transmitter_configs but did not run the executor).
	for sel, dc := range out {
		if dc.offRampContractID == "" || dc.stateChangedTopic == "" {
			delete(out, sel)
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// CreateStellarAccessorFactory is registered with chainaccess.Register for the Stellar family.
// Per-chain reader settings (RPC URL, network passphrase) come from the bind-mounted Stellar
// TOML written by the devenv modifiers; on-ramp / RMN remote hex addresses come from the job
// spec via GenericConfig. When the GenericConfig also carries an executor ChainConfiguration,
// the Stellar accessor additionally exposes DestinationReader and ContractTransmitter
// capabilities for those chains.
//
// The job spec no longer carries a Stellar blockchain_infos section (removed upstream in
// chainlink-ccv changelog/2026-07-14_blockchain_infos_removal.md). Nothing is lost: it only
// ever duplicated network_passphrase and soroban_rpc_url, both of which the modifiers already
// write into the mounted TOML.
func CreateStellarAccessorFactory(lggr logger.Logger, genericConfig chainaccess.GenericConfig) (chainaccess.AccessorFactory, error) {
	configPath := stellarConfigPath()
	stellarFileCfg, err := loadStellarFileConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load stellar config: %w", err)
	}

	readerConfigs := make(map[string]sourcereader.ReaderConfig, len(stellarFileCfg.ReaderConfigs))
	maps.Copy(readerConfigs, stellarFileCfg.ReaderConfigs)
	if err := applyOnRampRMNHexOverrides(readerConfigs, genericConfig.OnRampAddresses, genericConfig.RMNRemoteAddresses); err != nil {
		return nil, err
	}

	destConfigs, err := buildStellarDestConfigs(stellarFileCfg, genericConfig)
	if err != nil {
		return nil, err
	}

	return NewFactory(lggr, readerConfigs, destConfigs, genericConfig.MaxRetryDuration), nil
}

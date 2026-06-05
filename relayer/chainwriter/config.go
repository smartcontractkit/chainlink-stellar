package chainwriter

import (
	"encoding/json"
	"fmt"

	"github.com/smartcontractkit/chainlink-common/pkg/types/stellar"
)

type ChainWriterConfig struct {
	Contracts map[string]*ContractConfig `json:"contracts"`
}

type ContractConfig struct {
	Name      string                     `json:"name,omitempty"`
	Functions map[string]*FunctionConfig `json:"functions"`
}

type FunctionConfig struct {
	FromAddress string `json:"fromAddress"`
}

func ParseConfig(configBytes []byte) (ChainWriterConfig, error) {
	var cfg stellar.ContractWriterConfig
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return ChainWriterConfig{}, fmt.Errorf("failed to unmarshal chain writer config: %w", err)
	}

	chainConfig := ChainWriterConfig{
		Contracts: make(map[string]*ContractConfig),
	}

	for contractName, contract := range cfg.Contracts {
		functions := make(map[string]*FunctionConfig)
		for funcName, function := range contract.Functions {
			functions[funcName] = &FunctionConfig{
				FromAddress: function.FromAddress,
			}
		}
		chainConfig.Contracts[contractName] = &ContractConfig{
			Name:      contract.Name,
			Functions: functions,
		}
	}

	return chainConfig, nil
}

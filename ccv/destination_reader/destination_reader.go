package destinationreader

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
)

var _ chainaccess.DestinationReader = (*DestinationReader)(nil)

// Config is the configuration required to create a Stellar destination reader.
type Config struct {
	// OffRampContractID is the Stellar contract address of the OffRamp.
	OffRampContractID string `toml:"offramp_contract_id"`
	// RMNRemoteContractID is the Stellar contract address of the RMN Remote.
	RMNRemoteContractID string `toml:"rmn_remote_contract_id"`
}

// DestinationReader reads execution state and CCV data from the Stellar OffRamp contract.
type DestinationReader struct {
	lggr                   *zerolog.Logger
	invoker                bindings.Invoker
	offrampClient          *offrampbindings.OffRampClient
	rmnRemoteAddress       string
	executionAttemptPoller *StellarExecutionAttemptPoller
}

// New creates a new Stellar DestinationReader.
func New(
	invoker bindings.Invoker,
	rpcClient ccvclient.RPCClient,
	offRampContractID string,
	rmnRemoteContractID string,
	lggr *zerolog.Logger,
	attemptCacheExpiration time.Duration,
) (*DestinationReader, error) {
	if invoker == nil {
		return nil, fmt.Errorf("invoker is required")
	}
	if rpcClient == nil {
		return nil, fmt.Errorf("rpc client is required")
	}
	if offRampContractID == "" {
		return nil, fmt.Errorf("offramp contract ID is required")
	}
	if rmnRemoteContractID == "" {
		return nil, fmt.Errorf("rmn remote contract ID is required")
	}
	if lggr == nil {
		return nil, fmt.Errorf("logger is required")
	}

	poller, err := NewStellarExecutionAttemptPoller(rpcClient, offRampContractID, lggr, attemptCacheExpiration)
	if err != nil {
		return nil, fmt.Errorf("failed to create execution attempt poller: %w", err)
	}

	return &DestinationReader{
		lggr:                   lggr,
		invoker:                invoker,
		offrampClient:          offrampbindings.NewOffRampClient(invoker, offRampContractID),
		rmnRemoteAddress:       rmnRemoteContractID,
		executionAttemptPoller: poller,
	}, nil
}

// Start implements services.Service.
func (d *DestinationReader) Start(ctx context.Context) error {
	if err := d.executionAttemptPoller.Start(ctx); err != nil {
		return fmt.Errorf("failed to start execution attempt poller: %w", err)
	}
	d.lggr.Info().Msg("Stellar DestinationReader started")
	return nil
}

// Close implements services.Service.
func (d *DestinationReader) Close() error {
	if err := d.executionAttemptPoller.Close(); err != nil {
		d.lggr.Error().Err(err).Msg("Failed to stop execution attempt poller")
	}
	d.lggr.Info().Msg("Stellar DestinationReader stopped")
	return nil
}

// Ready implements services.Service.
func (d *DestinationReader) Ready() error {
	return nil
}

// HealthReport implements services.Service.
func (d *DestinationReader) HealthReport() map[string]error {
	report := map[string]error{"StellarDestinationReader": nil}
	for k, v := range d.executionAttemptPoller.HealthReport() {
		report[k] = v
	}
	return report
}

// Name implements services.Service.
func (d *DestinationReader) Name() string {
	return "StellarDestinationReader"
}

// GetMessageSuccess queries the OffRamp contract for the execution state of a message
// and returns true if the message has been successfully executed.
func (d *DestinationReader) GetMessageSuccess(ctx context.Context, message protocol.Message) (bool, error) {
	msgID, err := message.MessageID()
	if err != nil {
		return false, fmt.Errorf("failed to compute message ID: %w", err)
	}
	state, err := d.offrampClient.GetExecutionState(ctx, msgID)
	if err != nil {
		return false, fmt.Errorf("failed to get execution state for message %x: %w", msgID, err)
	}
	return state == offrampbindings.MessageExecutionStateSuccess, nil
}

// GetCCVSForMessage returns the cross-chain verification addresses for the message.
// It queries the OffRamp's source chain config to determine which CCVs are required
// (lane-mandated) and which are optional (defaults). The Stellar OffRamp quorum logic
// requires all lane-mandated CCVs plus at least one default CCV to verify.
func (d *DestinationReader) GetCCVSForMessage(ctx context.Context, message protocol.Message) (protocol.CCVAddressInfo, error) {
	sourceSelector := uint64(message.SourceChainSelector)

	sourceConfig, err := d.offrampClient.GetSourceChainConfig(ctx, sourceSelector)
	if err != nil {
		return protocol.CCVAddressInfo{}, fmt.Errorf("failed to get source chain config for selector %d: %w", sourceSelector, err)
	}

	requiredCCVs := make([]protocol.UnknownAddress, len(sourceConfig.LaneMandatedCcvs))
	for i, addr := range sourceConfig.LaneMandatedCcvs {
		parsedAddr := scval.ParseAddress(addr)
		if parsedAddr == nil {
			return protocol.CCVAddressInfo{}, fmt.Errorf("failed to parse address: %s", addr)
		}
		requiredCCVs[i] = protocol.UnknownAddress((*parsedAddr.ContractId)[:])
	}

	optionalCCVs := make([]protocol.UnknownAddress, len(sourceConfig.DefaultCcvs))
	for i, addr := range sourceConfig.DefaultCcvs {
		parsedAddr := scval.ParseAddress(addr)
		if parsedAddr == nil {
			return protocol.CCVAddressInfo{}, fmt.Errorf("failed to parse address: %s", addr)
		}
		optionalCCVs[i] = protocol.UnknownAddress((*parsedAddr.ContractId)[:])
	}

	var optionalThreshold uint8
	if len(optionalCCVs) > 0 {
		optionalThreshold = 1
	}

	ccvInfo := protocol.CCVAddressInfo{
		RequiredCCVs:      requiredCCVs,
		OptionalCCVs:      optionalCCVs,
		OptionalThreshold: optionalThreshold,
	}

	d.lggr.Info().
		Uint64("sourceChainSelector", sourceSelector).
		Int("requiredCCVs", len(requiredCCVs)).
		Int("optionalCCVs", len(optionalCCVs)).
		Uint8("optionalThreshold", optionalThreshold).
		Msg("Resolved CCV info for message")

	return ccvInfo, nil
}

// GetExecutionAttempts retrieves execution attempts for the given message from the poller cache.
func (d *DestinationReader) GetExecutionAttempts(ctx context.Context, message protocol.Message) ([]protocol.ExecutionAttempt, error) {
	return d.executionAttemptPoller.GetExecutionAttempts(ctx, message)
}

// GetRMNCursedSubjects queries the RMN Remote contract for cursed subjects.
func (d *DestinationReader) GetRMNCursedSubjects(ctx context.Context) ([]protocol.Bytes16, error) {
	rmnRemoteClient := rmnremotebindings.NewRmnRemoteClient(d.invoker, d.rmnRemoteAddress)
	cursedSubjects, err := rmnRemoteClient.GetCursedSubjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get cursed subjects: %w", err)
	}

	result := make([]protocol.Bytes16, len(cursedSubjects))
	for i, s := range cursedSubjects {
		result[i] = protocol.Bytes16(s)
	}
	return result, nil
}

// CheckHealth implements chainaccess.DestinationReader. Currently always returns nil.
// TODO: Implement this.
func (d *DestinationReader) CheckHealth(chain protocol.ChainSelector) error {
	return nil
}

// HasHonestAttempt implements chainaccess.DestinationReader. Currently always returns false. Returning false
// means that the offchain will attempt to submit an execution attempt for this message.
// TODO: Implement this.
func (d *DestinationReader) HasHonestAttempt(ctx context.Context, message protocol.Message, verifierResults []protocol.VerifierResult, ccvAddressInfo protocol.CCVAddressInfo) (bool, error) {
	return false, nil
}

// IsReady implements chainaccess.DestinationReader. Currently always returns true.
// TODO: Implement this.
func (d *DestinationReader) IsReady(chain protocol.ChainSelector) bool {
	return true
}

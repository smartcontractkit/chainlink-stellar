package contracttransmitter

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog"

	"github.com/smartcontractkit/chainlink-ccv/executor"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

var secp256k1N = crypto.S256().Params().N
var secp256k1HalfN = new(big.Int).Rsh(secp256k1N, 1)

// DefaultGasLimitOverride is passed to every offramp execute call.
// Zero means "use the gas limit from the message itself".
const DefaultGasLimitOverride uint32 = 0

var _ chainaccess.ContractTransmitter = (*ContractTransmitter)(nil)

// ContractTransmitterConfig is the configuration required to create a Stellar contract transmitter.
type ContractTransmitterConfig struct {
	// NetworkPassphrase is the Stellar network passphrase (e.g., "Standalone Network ; February 2017").
	NetworkPassphrase string `toml:"network_passphrase"`
	// OffRampContractID is the contract ID of the Stellar OffRamp contract.
	OffRampContractID string `toml:"offramp_contract_id"`
	// CCIPOfframpAddress is the address of the CCIP OffRamp contract.
	CCIPOfframpAddress string `toml:"ccip_offramp_address"`
	// CCIPStateChangedTopic is the topic of the CCIP StateChanged event.
	CCIPStateChangedTopic string `toml:"ccip_state_changed_topic"`
	// RMNRemoteAddress is the address of the RMN Remote contract.
	RMNRemoteAddress string `toml:"rmn_remote_address"`
}

// ContractTransmitter transmits aggregated reports to the Stellar OffRamp
// contract by calling its `execute` entry point via a Soroban invoker.
type ContractTransmitter struct {
	lggr                  *zerolog.Logger
	invoker               bindings.Invoker
	ccipOfframpAddress    string
	ccipStateChangedTopic string
	rmnRemoteAddress      string
	offrampClient         *offrampbindings.OffRampClient
}

// NewContractTransmitter creates a Stellar ContractTransmitter.
func NewContractTransmitterWithClient(
	invoker bindings.Invoker,
	ccipOfframpAddress string,
	ccipStateChangedTopic string,
	rmnRemoteAddress string,
	lggr *zerolog.Logger,
) (*ContractTransmitter, error) {
	if invoker == nil {
		return nil, fmt.Errorf("invoker is required")
	}
	if ccipOfframpAddress == "" {
		return nil, fmt.Errorf("ccip offramp address is required")
	}
	if ccipStateChangedTopic == "" {
		return nil, fmt.Errorf("ccip state changed topic is required")
	}
	if lggr == nil {
		return nil, fmt.Errorf("logger is required")
	}

	offrampClient := offrampbindings.NewOffRampClient(invoker, ccipOfframpAddress)

	return &ContractTransmitter{
		invoker:               invoker,
		ccipOfframpAddress:    ccipOfframpAddress,
		ccipStateChangedTopic: ccipStateChangedTopic,
		rmnRemoteAddress:      rmnRemoteAddress,
		lggr:                  lggr,
		offrampClient:         offrampClient,
	}, nil
}

// convertVerifierBlobToEIP2098 converts a single verifier result blob from the
// CCV aggregator's v=27-normalized R||S format to EIP-2098 compact format.
//
// Blob layout: [4B version_tag][2B BE sig_len][N × 64B R||S pairs]
//
// For each 64-byte signature, if the v=27-normalized S > n/2, the original
// recovery ID was 1 and S was flipped (S_norm = n − S_orig). We undo the flip
// and encode the recovery ID in bit 255 of the S word per EIP-2098.
func convertVerifierBlobToEIP2098(blob []byte) ([]byte, error) {
	const headerLen = 6 // 4 version + 2 sigLen
	const sigSize = 64

	if len(blob) < headerLen {
		return blob, nil
	}

	sigPayloadLen := int(binary.BigEndian.Uint16(blob[4:6]))
	if sigPayloadLen == 0 || len(blob) < headerLen+sigPayloadLen {
		return blob, nil
	}
	if sigPayloadLen%sigSize != 0 {
		return nil, fmt.Errorf("signature payload length %d is not a multiple of %d", sigPayloadLen, sigSize)
	}

	out := make([]byte, len(blob))
	copy(out, blob)

	sigCount := sigPayloadLen / sigSize
	for i := range sigCount {
		sOff := headerLen + i*sigSize + 32
		sBytes := out[sOff : sOff+32]

		s := new(big.Int).SetBytes(sBytes)

		if s.Cmp(secp256k1HalfN) > 0 {
			// Original recovery_id was 1; undo the v=27 flip: S_orig = n − S_norm
			s.Sub(secp256k1N, s)
			padded := s.Bytes()
			clear(sBytes)
			copy(sBytes[32-len(padded):], padded)
			sBytes[0] |= 0x80 // set bit 255 (recovery_id = 1)
		}
		// If S <= n/2 the original recovery_id was 0 and the high bit is naturally clear.
	}

	return out, nil
}

// ConvertAndWriteMessageToChain encodes the report into ScVal arguments and
// invokes OffRamp.execute on Stellar.
//
// Stellar OffRamp.execute signature (Rust):
//
//	execute(env, encoded_message: Bytes, ccvs: Vec<Address>,
//	        verifier_results: Vec<Bytes>, gas_limit_override: u32)
func (ct *ContractTransmitter) ConvertAndWriteMessageToChain(ctx context.Context, report protocol.AbstractAggregatedReport) error {
	messageID := report.Message.MustMessageID()

	encodedMsg, err := report.Message.Encode()
	if err != nil {
		ct.lggr.Error().Err(err).
			Str("messageID", messageID.String()).
			Msg("Unable to submit txn: invalid message encoding")
		return errors.Join(executor.ErrMessageEncoding,
			fmt.Errorf("unable to submit txn: invalid message encoding: %w", err))
	}

	ccvScVals := make([]string, len(report.CCVS))
	for i, ccv := range report.CCVS {
		// CCV addresses from EVM are 32-byte hex values. The OffRamp expects Stellar Address (Vec<Address>),
		// which requires Stellar strkey format (C...). ParseAddress only accepts C... or G... strings;
		// passing raw bytes as string causes ParseAddress to return nil, leading to nil pointer panic during XDR encoding.
		ccvStrkey, convErr := scval.HexToContractStrkey("0x" + hex.EncodeToString(ccv.Bytes()))
		if convErr != nil {
			ct.lggr.Error().Err(convErr).
				Str("messageID", messageID.String()).
				Msg("Unable to submit txn: invalid CCV address encoding")
			return errors.Join(executor.ErrMessageEncoding,
				fmt.Errorf("unable to submit txn: invalid CCV address at index %d: %w", i, convErr))
		}
		ccvScVals[i] = ccvStrkey
	}

	// The committee aggregator encodes signatures as plain R||S pairs with v=27
	// normalization (matching EVM's ecrecover(hash, 27, r, s) convention). The
	// Stellar committee verifier instead parses EIP-2098 compact format where
	// bit 255 of the S word carries the recovery ID. This conversion bridges
	// the two representations so secp256k1_recover on-chain recovers the
	// correct signer addresses.
	convertedCCVData := make([][]byte, len(report.CCVData))
	for i, blob := range report.CCVData {
		converted, convErr := convertVerifierBlobToEIP2098(blob)
		if convErr != nil {
			ct.lggr.Error().Err(convErr).
				Str("messageID", messageID.String()).
				Int("blobIndex", i).
				Msg("Unable to submit txn: EIP-2098 signature conversion failed")
			return errors.Join(executor.ErrMessageEncoding,
				fmt.Errorf("unable to convert verifier blob %d to EIP-2098: %w", i, convErr))
		}
		convertedCCVData[i] = converted
	}

	err = ct.offrampClient.Execute(ctx, encodedMsg, ccvScVals, convertedCCVData, DefaultGasLimitOverride)

	if err != nil {
		ct.lggr.Error().
			Err(err).
			Str("messageID", messageID.String()).
			Msg("Unable to submit txn: offramp client execute failed")
	}

	return err
}

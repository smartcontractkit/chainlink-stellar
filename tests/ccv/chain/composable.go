package ccvchain

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	common "github.com/smartcontractkit/chainlink-stellar/ccv/common"
)

// StellarSendOptions is the cciptestinterfaces.ChainSendOption implementation for Stellar.
// Fields may be extended (e.g. alternate signer); unknown sendOption values are ignored.
type StellarSendOptions struct{}

// IsSendOption implements cciptestinterfaces.ChainSendOption.
//
// Per chainlink-ccv changelog/2026-04-27_extra_args_data_provider.md the marker
// no longer returns a bool — the previous return value was never inspected.
func (StellarSendOptions) IsSendOption() {}

var (
	_ cciptestinterfaces.ChainAsSource        = (*Chain)(nil)
	_ cciptestinterfaces.ChainAsDestination   = (*Chain)(nil)
	_ cciptestinterfaces.MessageV3Destination = (*Chain)(nil)
	_ cciptestinterfaces.MessageV3Source      = (*Chain)(nil)
	_ cciptestinterfaces.V3Source             = (*Chain)(nil)
)

// BuildV3ExtraArgs implements cciptestinterfaces.MessageV3Source: merges the
// destination chain's executor args, token receiver, and token args into opts
// and serializes everything into the Soroban GenericExtraArgsV3 XDR blob the
// Stellar OnRamp parses. tcapi cases call this before BuildChainMessage, so
// opts.CCVs/opts.Executor arrive hydrated from the SOURCE-side resolver
// (ResolveV3SendAddresses) — 32-byte Soroban addresses that only the Stellar
// encoding can express.
func (c *Chain) BuildV3ExtraArgs(
	opts cciptestinterfaces.MessageOptions,
	destChain cciptestinterfaces.MessageV3Destination,
	executorArgsParams any,
	tokenReceiverParams any,
	tokenArgsParams any,
) (cciptestinterfaces.GenericExtraArgs, error) {
	executorArgs, err := destChain.GetExecutorArgs(executorArgsParams)
	if err != nil {
		return nil, fmt.Errorf("get executor args from destination chain: %w", err)
	}
	opts.ExecutorArgs = executorArgs

	tokenReceiver, err := destChain.GetTokenReceiver(tokenReceiverParams)
	if err != nil {
		return nil, fmt.Errorf("get token receiver from destination chain: %w", err)
	}
	opts.TokenReceiver = tokenReceiver

	tokenArgs, err := destChain.GetTokenArgs(tokenArgsParams)
	if err != nil {
		return nil, fmt.Errorf("get token args from destination chain: %w", err)
	}
	opts.TokenArgs = tokenArgs

	// CCIP devenv policy: allow out-of-order execution on the destination path.
	// The Soroban GenericExtraArgsV3 struct has no OOO field today; this matches
	// the fallback path in BuildChainMessage.
	opts.OutOfOrderExecution = true

	encoded, err := EncodeStellarSourceExtraArgsForOnRamp(c.vvrContractID, opts)
	if err != nil {
		return nil, fmt.Errorf("encode V3 extra args for Stellar OnRamp: %w", err)
	}
	return cciptestinterfaces.GenericExtraArgs(encoded), nil
}

// BuildChainMessage implements cciptestinterfaces.ChainAsSource.
//
// The pre-2026-04-27 signature took a destination chain selector and a
// MessageOptions struct, and the source was responsible for serialising the
// extra args. The new signature receives pre-serialised GenericExtraArgs from
// the caller (load gun / scenario / CLI) along with destination-family
// awareness via the (family, version) lookup.
//
// extraArgs produced by BuildV3ExtraArgs (or EncodeStellarSourceExtraArgsForOnRamp)
// is already the Soroban GenericExtraArgsV3 XDR the Stellar OnRamp parses, so it
// is used verbatim. Empty extraArgs (e.g. CCIP17 SendMessage with a data
// provider) falls back to the out-of-order default encoding — a destination's
// wire format (e.g. EVM ABI) is never usable here.
func (c *Chain) BuildChainMessage(ctx context.Context, fields cciptestinterfaces.MessageFields, extraArgs cciptestinterfaces.GenericExtraArgs) (cciptestinterfaces.GenericChainMessage, error) {
	_ = ctx

	var encodedExtraArgs []byte
	if len(extraArgs) > 0 {
		encodedExtraArgs = []byte(extraArgs)
	} else {
		// CCIP devenv policy: allow out-of-order execution on the destination
		// path. The Soroban GenericExtraArgsV3 struct has no OOO field today;
		// we pre-populate a MessageOptions so EncodeStellarSourceExtraArgsForOnRamp
		// emits sensible defaults. Callers that need richer per-send overrides
		// should construct the Soroban extraArgs externally.
		var err error
		encodedExtraArgs, err = EncodeStellarSourceExtraArgsForOnRamp(
			c.vvrContractID,
			cciptestinterfaces.MessageOptions{OutOfOrderExecution: true},
		)
		if err != nil {
			return nil, fmt.Errorf("encode extra args for Stellar OnRamp: %w", err)
		}
	}

	return c.buildStellarMessageBody(fields, encodedExtraArgs)
}

// BuildStellarMessageWithExecutor builds a StellarToAnyMessage with a
// caller-supplied [cciptestinterfaces.MessageOptions] (notably a custom
// Executor), for e2e tests that must drive the OnRamp's executor-sentinel
// resolution (M-5 use-default / M-7 no-execution). Unlike BuildChainMessage,
// which defaults the executor to the use-default sentinel, this honors
// opts.Executor (a 32-byte
// Soroban address or sentinel), opts.ExecutionGasLimit, opts.CCVs, etc. The
// caller should set opts.OutOfOrderExecution = true to match devenv policy.
func (c *Chain) BuildStellarMessageWithExecutor(
	_ context.Context,
	fields cciptestinterfaces.MessageFields,
	opts cciptestinterfaces.MessageOptions,
) (routerbindings.StellarToAnyMessage, error) {
	encodedExtraArgs, err := EncodeStellarSourceExtraArgsForOnRamp(
		c.vvrContractID,
		opts,
	)
	if err != nil {
		return routerbindings.StellarToAnyMessage{}, fmt.Errorf("encode extra args for Stellar OnRamp: %w", err)
	}
	return c.buildStellarMessageBody(fields, encodedExtraArgs)
}

// buildStellarMessageBody is the shared StellarToAnyMessage construction
// (fee-token resolution + token-amount i128 guard) used by both
// BuildChainMessage and BuildStellarMessageWithExecutor.
func (c *Chain) buildStellarMessageBody(
	fields cciptestinterfaces.MessageFields,
	encodedExtraArgs []byte,
) (routerbindings.StellarToAnyMessage, error) {
	if c.feeTokenContractID == "" {
		return routerbindings.StellarToAnyMessage{}, fmt.Errorf("fee token not deployed; run DeployContractsForSelector first")
	}
	feeToken := c.feeTokenContractID
	if len(fields.FeeToken) > 0 {
		ft, encErr := strkey.Encode(strkey.VersionByteContract, []byte(fields.FeeToken))
		if encErr != nil {
			return routerbindings.StellarToAnyMessage{}, fmt.Errorf("encode fee token address: %w", encErr)
		}
		feeToken = ft
	}

	var tokenAmounts []routerbindings.TokenAmount
	if fields.TokenAmount.Amount != nil && fields.TokenAmount.Amount.Sign() > 0 && len(fields.TokenAmount.TokenAddress) > 0 {
		// TokenAmount.Amount is a *big.Int from the external CCV test interface.
		// The on-chain field is i128, so reject out-of-range amounts here with
		// an error rather than accepting the message and letting scval.I128ToScVal
		// panic during send. This restores the fail-fast guard dropped when Amount
		// widened from int64 to *big.Int. The window matches scval.I128ToScVal's
		// acceptance range [-(2^127-1), 2^127]; only the upper bound is reachable
		// here because Sign() > 0 already excludes non-positive values.
		i128Max := new(big.Int).Lsh(big.NewInt(1), 127)
		i128Min := new(big.Int).Neg(new(big.Int).Sub(i128Max, big.NewInt(1)))
		if fields.TokenAmount.Amount.Cmp(i128Min) < 0 || fields.TokenAmount.Amount.Cmp(i128Max) > 0 {
			return routerbindings.StellarToAnyMessage{}, fmt.Errorf("token amount out of i128 range: %s", fields.TokenAmount.Amount.String())
		}
		tokenAddr, encErr := strkey.Encode(strkey.VersionByteContract, []byte(fields.TokenAmount.TokenAddress))
		if encErr != nil {
			return routerbindings.StellarToAnyMessage{}, fmt.Errorf("encode token address for send: %w", encErr)
		}
		tokenAmounts = []routerbindings.TokenAmount{{
			Token:  tokenAddr,
			Amount: fields.TokenAmount.Amount,
		}}
	}

	return routerbindings.StellarToAnyMessage{
		Receiver:     fields.Receiver,
		Data:         fields.Data,
		TokenAmounts: tokenAmounts,
		FeeToken:     feeToken,
		ExtraArgs:    encodedExtraArgs,
	}, nil
}

// SendChainMessage implements cciptestinterfaces.ChainAsSource.
// msg must be the routerbindings.StellarToAnyMessage returned from BuildChainMessage.
func (c *Chain) SendChainMessage(ctx context.Context, destChain uint64, msg cciptestinterfaces.GenericChainMessage, sendOption cciptestinterfaces.ChainSendOption) (cciptestinterfaces.MessageSentEvent, protocol.ByteSlice, error) {
	// Optional StellarSendOptions; other ChainSendOption types are ignored (EVM-style defaults).
	if _, ok := sendOption.(StellarSendOptions); ok {
		// Reserved for future per-send overrides.
	}
	routerMsg, ok := msg.(routerbindings.StellarToAnyMessage)
	if !ok {
		return cciptestinterfaces.MessageSentEvent{}, nil, fmt.Errorf("expected routerbindings.StellarToAnyMessage, got %T", msg)
	}
	if c.routerClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, nil, fmt.Errorf("Router client not initialized")
	}
	sender := c.deployerKeypair.Address()

	requiredFee, err := c.routerClient.GetFee(ctx, destChain, routerMsg)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, nil, fmt.Errorf("get fee from Router: %w", err)
	}
	c.logger.Info().Str("requiredFee", requiredFee.String()).Msg("Fee quote from Router (SendChainMessage)")

	messageID, err := c.routerClient.CcipSend(ctx, sender, destChain, routerMsg)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, nil, fmt.Errorf("ccip_send: %w", err)
	}
	c.logger.Info().
		Str("messageID", common.HexEncode(messageID[:])).
		Msg("CCIP message sent from Stellar via Router (SendChainMessage)")

	// Populate ReceiptIssuers from the OnRamp CCIPMessageSent event (EVM parity:
	// there the send tx receipt carries the event synchronously; on Stellar we
	// wait for the ledger that includes the send tx). tcapi cases assert the
	// receipt-issuer count (CCV + executor + network = 3 for a 1-CCV data-only
	// send), so the send fails loudly if the event cannot be observed. CcipSend
	// blocks until tx inclusion, so scan from a couple of ledgers before the
	// current latest: a ledger closing in between must not hide the event.
	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, nil,
			fmt.Errorf("get latest ledger for sent-event wait: %w", err)
	}
	startLedger := latestLedger.Sequence
	if startLedger > sentEventLookbackLedgers {
		startLedger -= sentEventLookbackLedgers
	}
	wantMessageID := messageID
	event, err := c.waitForCCIPMessageSentEvent(ctx, startLedger, sendReceiptWaitTimeout,
		func(e *CCIPMessageSentEvent) bool {
			return e.DestChainSelector == destChain && e.MessageId == wantMessageID
		})
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, nil,
			fmt.Errorf("wait for sent event to populate receipt issuers: %w", err)
	}
	receiptIssuers := make([]protocol.UnknownAddress, 0, len(event.Receipts))
	for i, r := range event.Receipts {
		raw, decErr := strkey.Decode(strkey.VersionByteContract, r.Issuer)
		if decErr != nil {
			return cciptestinterfaces.MessageSentEvent{}, nil,
				fmt.Errorf("decode receipt issuer %d %q: %w", i, r.Issuer, decErr)
		}
		receiptIssuers = append(receiptIssuers, protocol.UnknownAddress(raw))
	}

	// Soroban deployer does not currently plumb transaction hash through CcipSend; composable
	// helpers treat tx hash as optional.
	return cciptestinterfaces.MessageSentEvent{
		MessageID:      messageID,
		Sender:         protocol.UnknownAddress([]byte(sender)),
		ReceiptIssuers: receiptIssuers,
	}, nil, nil
}

// sendReceiptWaitTimeout bounds the CCIPMessageSent event wait in
// SendChainMessage. Quickstart ledgers close every few seconds; the bound only
// needs to cover ledger close plus event indexing.
const sendReceiptWaitTimeout = 30 * time.Second

// sentEventLookbackLedgers backs the start ledger of the CCIPMessageSent
// event wait in SendChainMessage (see the comment there).
const sentEventLookbackLedgers uint32 = 2

// GetExecutorArgs implements cciptestinterfaces.MessageV3Destination.
// Returns empty executor args for Stellar (executor args are destination-specific).
func (c *Chain) GetExecutorArgs(opts any) (cciptestinterfaces.MessageV3ExecutorArgs, error) {
	// For Stellar, executor args are not used as the destination determines execution
	// Return nil to indicate no executor args needed
	// TODO: verify
	return nil, nil
}

// GetTokenReceiver implements cciptestinterfaces.MessageV3Destination.
// Returns nil for Stellar as token receiver is typically the same as message receiver.
func (c *Chain) GetTokenReceiver(opts any) (cciptestinterfaces.MessageV3TokenReceiver, error) {
	// For Stellar, token receiver is typically the same as the message receiver
	// Return nil to indicate no distinct token receiver
	// TODO: verify
	return nil, nil
}

// GetTokenArgs implements cciptestinterfaces.MessageV3Destination.
// Returns empty token args for Stellar (token args are destination-specific).
func (c *Chain) GetTokenArgs(opts any) (cciptestinterfaces.MessageV3TokenArgs, error) {
	// For Stellar, token args are not used as the destination determines token handling
	// Return nil to indicate no token args needed
	// TODO: verify
	return nil, nil
}

package ccvchain

import (
	"context"
	"fmt"
	"math/big"

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
)

// BuildChainMessage implements cciptestinterfaces.ChainAsSource.
//
// The pre-2026-04-27 signature took a destination chain selector and a
// MessageOptions struct, and the source was responsible for serialising the
// extra args. The new signature receives pre-serialised GenericExtraArgs from
// the caller (load gun / scenario / CLI) along with destination-family
// awareness via the (family, version) lookup. For Stellar-as-source the
// pre-serialised extra args are not directly usable because the Stellar
// OnRamp expects Soroban GenericExtraArgsV3 XDR rather than the destination's
// wire format, so we ignore the provided extraArgs and re-encode using the
// Stellar-side helper.
func (c *Chain) BuildChainMessage(ctx context.Context, fields cciptestinterfaces.MessageFields, extraArgs cciptestinterfaces.GenericExtraArgs) (cciptestinterfaces.GenericChainMessage, error) {
	_ = ctx
	_ = extraArgs

	// CCIP devenv policy: allow out-of-order execution on the destination
	// path. The Soroban GenericExtraArgsV3 struct has no OOO field today; we
	// pre-populate a MessageOptions so EncodeStellarSourceExtraArgsForOnRamp
	// emits sensible defaults. Callers that need richer per-send overrides
	// should construct the Soroban extraArgs externally.
	encodedExtraArgs, err := EncodeStellarSourceExtraArgsForOnRamp(
		c.vvrContractID,
		cciptestinterfaces.MessageOptions{OutOfOrderExecution: true},
	)
	if err != nil {
		return nil, fmt.Errorf("encode extra args for Stellar OnRamp: %w", err)
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

	messageID, err := c.routerClient.CcipSend(ctx, sender, destChain, routerMsg, requiredFee)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, nil, fmt.Errorf("ccip_send: %w", err)
	}
	c.logger.Info().
		Str("messageID", common.HexEncode(messageID[:])).
		Msg("CCIP message sent from Stellar via Router (SendChainMessage)")

	// Soroban deployer does not currently plumb transaction hash through CcipSend; composable
	// helpers treat tx hash as optional.
	return cciptestinterfaces.MessageSentEvent{
		MessageID: messageID,
		Sender:    protocol.UnknownAddress([]byte(sender)),
	}, nil, nil
}

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

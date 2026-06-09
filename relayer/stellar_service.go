package relayer

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	relaytypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

// stellarTxManager is the subset of txm.StellarTxm used by SubmitTransaction.
// Defining a narrow interface here keeps stellarService testable without a live TXM.
type stellarTxManager interface {
	EnqueueAndWait(ctx context.Context, req txm.TxRequest) (*txm.TxResult, error)
}

type stellarService struct {
	relaytypes.UnimplementedStellarService
	chain chain.Chain
	txMgr stellarTxManager
}

var _ relaytypes.StellarService = (*stellarService)(nil)

func newStellarService(ch chain.Chain) stellarService {
	return stellarService{chain: ch, txMgr: ch.TxManager()}
}

func (s *stellarService) GetLedgerEntries(ctx context.Context, req stellartypes.GetLedgerEntriesRequest) (stellartypes.GetLedgerEntriesResponse, error) {
	rpc, err := s.chain.GetClient()
	if err != nil {
		return stellartypes.GetLedgerEntriesResponse{}, fmt.Errorf("GetLedgerEntries: get client: %w", err)
	}

	keys := make([]string, len(req.Keys))
	for i, k := range req.Keys {
		keys[i] = k
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
	if err != nil {
		return stellartypes.GetLedgerEntriesResponse{}, fmt.Errorf("GetLedgerEntries: %w", err)
	}

	entries := make([]stellartypes.LedgerEntryResult, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		entry := stellartypes.LedgerEntryResult{
			KeyXDR:             e.KeyXDR,
			DataXDR:            e.DataXDR,
			LastModifiedLedger: e.LastModifiedLedger,
			ExtensionXDR:       e.ExtensionXDR,
		}
		if e.LiveUntilLedgerSeq != nil {
			v := *e.LiveUntilLedgerSeq
			entry.LiveUntilLedgerSeq = &v
		}
		entries = append(entries, entry)
	}

	return stellartypes.GetLedgerEntriesResponse{
		Entries:      entries,
		LatestLedger: resp.LatestLedger,
	}, nil
}

func (s *stellarService) GetLatestLedger(ctx context.Context) (stellartypes.GetLatestLedgerResponse, error) {
	rpc, err := s.chain.GetClient()
	if err != nil {
		return stellartypes.GetLatestLedgerResponse{}, fmt.Errorf("failed to get client: %w", err)
	}

	resp, err := rpc.GetLatestLedger(ctx)
	if err != nil {
		return stellartypes.GetLatestLedgerResponse{}, err
	}

	return stellartypes.GetLatestLedgerResponse{
		Hash:              resp.Hash,
		ProtocolVersion:   resp.ProtocolVersion,
		Sequence:          resp.Sequence,
		LedgerCloseTime:   resp.LedgerCloseTime,
		LedgerHeaderXDR:   resp.LedgerHeader,
		LedgerMetadataXDR: resp.LedgerMetadata,
	}, nil
}

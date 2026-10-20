package txm

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/smartcontractkit/chainlink-common/pkg/beholder"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/metrics"
)

var (
	promStellarTxmBroadcastedTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_broadcasted",
		Help: "Number of transactions accepted by SendTransaction",
	}, []string{"chainID"})

	promStellarTxmSuccessTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_success",
		Help: "Number of transactions confirmed with GetTransaction SUCCESS",
	}, []string{"chainID"})

	promStellarTxmPendingTxs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stellar_txm_tx_pending",
		Help: "Current in-flight unconfirmed transactions",
	}, []string{"chainID"})

	promStellarTxmOutcomeTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_outcome",
		Help: "Transaction errors, retries, and drops by outcome and reason",
	}, []string{"chainID", "outcome", "reason"})

	promStellarTxmMaxAttemptsReached = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_max_attempts_reached",
		Help: "Transactions that exhausted their retry budget (lifecycle or infra) before failing",
	}, []string{"chainID", "budget"})

	promStellarTxmSubmitRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_submit_rejected",
		Help: "SendTransaction results that were not accepted, by reason (bad_seq, insufficient_fee, too_late, try_again_later, ...)",
	}, []string{"chainID", "reason"})

	promStellarTxmTimeUntilTxConfirmed = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "txm_time_until_tx_confirmed",
		Help: "The amount of time elapsed from a transaction being broadcast to being included in a ledger.",
	}, []string{"chainID"})

	// Stellar-specific metrics

	promStellarTxmRestore = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_restore",
		Help: "RestoreFootprint lifecycle events by outcome",
	}, []string{"chainID", "outcome"})

	promStellarTxmSimDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_simulation_duration_ms",
		Help:    "Time spent in SimulateTransaction calls, in milliseconds",
		Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096},
	}, []string{"chainID"})

	promStellarTxmFeeInclusion = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_fee_inclusion_stroops",
		Help:    "Inclusion fee paid (in stroops)",
		Buckets: []float64{100, 200, 500, 1000, 5000, 10000, 50000, 100000, 1000000, 5000000, 20000000, 200000000},
	}, []string{"chainID"})

	promStellarTxmFeeResource = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_fee_resource_stroops",
		Help:    "Resource fee charged (in stroops)",
		Buckets: []float64{10000, 50000, 100000, 500000, 1000000, 5000000, 10000000, 50000000, 200000000, 2000000000},
	}, []string{"chainID"})
)

// TxmMetrics is the metrics contract for the Stellar TXM transaction lifecycle.
type TxmMetrics interface {
	IncrementBroadcastedTxs(ctx context.Context, attempt uint64)
	IncrementSuccessTxs(ctx context.Context, attempt uint64)
	SetPendingTxs(context.Context, int)
	IncrementErrorTxs(context.Context, ErrorReason)
	IncrementRetryTxs(context.Context, RetryReason)
	IncrementDroppedTxs(context.Context, DropReason)
	IncrementMaxAttemptsReached(context.Context, RetryBudget)
	IncrementSubmitRejected(context.Context, ErrorReason)
	IncrementRestore(context.Context, RestoreOutcome)
	ObserveSimulationDuration(context.Context, int64)
	ObserveInclusionFee(context.Context, int64)
	ObserveResourceFee(context.Context, int64)
	ObserveRPCHeadLag(ctx context.Context, ledgers int64)
	RecordTimeUntilTxConfirmed(context.Context, float64)
}

type stellarTxmMetrics struct {
	metrics.Labeler
	chainID string

	// Transaction lifecycle metrics
	broadcastedTxs       metric.Int64Counter
	successTxs           metric.Int64Counter
	pendingTxs           metric.Int64Gauge
	outcomeTxs           metric.Int64Counter
	maxAttemptsReached   metric.Int64Counter
	submitRejected       metric.Int64Counter
	timeUntilTxConfirmed metric.Float64Histogram

	// Stellar-specific metrics
	restore      metric.Int64Counter
	simDuration  metric.Int64Histogram
	feeInclusion metric.Int64Histogram
	feeResource  metric.Int64Histogram
	rpcHeadLag   metric.Int64Histogram
}

func NewStellarTxmMetrics(lggr logger.Logger, chainID string) TxmMetrics {
	var initErr error
	meter := beholder.GetMeter()

	broadcastedTxs, err := meter.Int64Counter("stellar_txm_tx_broadcasted")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_broadcasted: %w", err))
	}

	successTxs, err := meter.Int64Counter("stellar_txm_tx_success")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_success: %w", err))
	}

	pendingTxs, err := meter.Int64Gauge("stellar_txm_tx_pending")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_pending: %w", err))
	}

	outcomeTxs, err := meter.Int64Counter("stellar_txm_tx_outcome")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_outcome: %w", err))
	}

	maxAttemptsReached, err := meter.Int64Counter("stellar_txm_tx_max_attempts_reached")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_max_attempts_reached: %w", err))
	}

	submitRejected, err := meter.Int64Counter("stellar_txm_tx_submit_rejected")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_tx_submit_rejected: %w", err))
	}

	timeUntilTxConfirmed, err := meter.Float64Histogram("txm_time_until_tx_confirmed")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("txm_time_until_tx_confirmed: %w", err))
	}

	restore, err := meter.Int64Counter("stellar_txm_restore")
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_restore: %w", err))
	}

	simDuration, err := meter.Int64Histogram("stellar_txm_simulation_duration_ms",
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096),
	)
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_simulation_duration_ms: %w", err))
	}

	feeInclusion, err := meter.Int64Histogram("stellar_txm_fee_inclusion_stroops",
		metric.WithExplicitBucketBoundaries(100, 200, 500, 1000, 5000, 10000, 50000, 100000, 1000000, 5000000, 20000000, 200000000),
	)
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_fee_inclusion_stroops: %w", err))
	}

	feeResource, err := meter.Int64Histogram("stellar_txm_fee_resource_stroops",
		metric.WithExplicitBucketBoundaries(10000, 50000, 100000, 500000, 1000000, 5000000, 10000000, 50000000, 200000000, 2000000000),
	)
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_fee_resource_stroops: %w", err))
	}

	rpcHeadLag, err := meter.Int64Histogram("stellar_txm_rpc_head_lag_ledgers",
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20),
	)
	if err != nil {
		initErr = errors.Join(initErr, fmt.Errorf("stellar_txm_rpc_head_lag_ledgers: %w", err))
	}

	if initErr != nil {
		lggr.Errorw("Failed to initialize Stellar TXM metrics; using noop metrics", "err", initErr)
		return NewNoopStellarTxmMetrics()
	}

	return &stellarTxmMetrics{
		chainID: chainID,
		Labeler: metrics.NewLabeler().With("chainID", chainID),

		broadcastedTxs:       broadcastedTxs,
		successTxs:           successTxs,
		pendingTxs:           pendingTxs,
		outcomeTxs:           outcomeTxs,
		maxAttemptsReached:   maxAttemptsReached,
		submitRejected:       submitRejected,
		timeUntilTxConfirmed: timeUntilTxConfirmed,

		restore:      restore,
		simDuration:  simDuration,
		feeInclusion: feeInclusion,
		feeResource:  feeResource,
		rpcHeadLag:   rpcHeadLag,
	}
}

func NewNoopStellarTxmMetrics() TxmMetrics {
	return noopStellarTxmMetrics{}
}

func (m *stellarTxmMetrics) getOtelAttributes() []attribute.KeyValue {
	return beholder.OtelAttributes(m.Labels).AsStringAttributes()
}

// --- Transaction lifecycle metrics ---

// IncrementBroadcastedTxs and IncrementSuccessTxs share the attempt attribute so the landing rate
// of each broadcast attempt is success{attempt} / broadcasted{attempt}.
func (m *stellarTxmMetrics) IncrementBroadcastedTxs(ctx context.Context, attempt uint64) {
	promStellarTxmBroadcastedTxs.WithLabelValues(m.chainID).Add(1)
	m.broadcastedTxs.Add(ctx, 1, metric.WithAttributes(m.attemptAttributes(attempt)...))
}

func (m *stellarTxmMetrics) IncrementSuccessTxs(ctx context.Context, attempt uint64) {
	promStellarTxmSuccessTxs.WithLabelValues(m.chainID).Add(1)
	m.successTxs.Add(ctx, 1, metric.WithAttributes(m.attemptAttributes(attempt)...))
}

const maxAttemptLabel = 3

func (m *stellarTxmMetrics) attemptAttributes(attempt uint64) []attribute.KeyValue {
	label := strconv.FormatUint(attempt, 10)
	if attempt >= maxAttemptLabel {
		label = strconv.Itoa(maxAttemptLabel) + "+"
	}
	return append(m.getOtelAttributes(), attribute.String("attempt", label))
}

func (m *stellarTxmMetrics) SetPendingTxs(ctx context.Context, count int) {
	promStellarTxmPendingTxs.WithLabelValues(m.chainID).Set(float64(count))
	m.pendingTxs.Record(ctx, int64(count), metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementErrorTxs(ctx context.Context, reason ErrorReason) {
	m.recordOutcome(ctx, "error", string(reason))
}

func (m *stellarTxmMetrics) IncrementRetryTxs(ctx context.Context, reason RetryReason) {
	m.recordOutcome(ctx, "retry", string(reason))
}

func (m *stellarTxmMetrics) IncrementDroppedTxs(ctx context.Context, reason DropReason) {
	m.recordOutcome(ctx, "drop", string(reason))
}

func (m *stellarTxmMetrics) IncrementMaxAttemptsReached(ctx context.Context, budget RetryBudget) {
	promStellarTxmMaxAttemptsReached.WithLabelValues(m.chainID, string(budget)).Add(1)
	otelAttrs := append(
		m.getOtelAttributes(),
		attribute.String("budget", string(budget)),
	)
	m.maxAttemptsReached.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) IncrementSubmitRejected(ctx context.Context, reason ErrorReason) {
	promStellarTxmSubmitRejected.WithLabelValues(m.chainID, string(reason)).Add(1)
	otelAttrs := append(
		m.getOtelAttributes(),
		attribute.String("reason", string(reason)),
	)
	m.submitRejected.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) recordOutcome(ctx context.Context, outcome, reason string) {
	promStellarTxmOutcomeTxs.WithLabelValues(m.chainID, outcome, reason).Add(1)
	otelAttrs := append(
		m.getOtelAttributes(),
		attribute.String("outcome", outcome),
		attribute.String("reason", reason),
	)
	m.outcomeTxs.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) RecordTimeUntilTxConfirmed(ctx context.Context, duration float64) {
	promStellarTxmTimeUntilTxConfirmed.WithLabelValues(m.chainID).Observe(duration)
	m.timeUntilTxConfirmed.Record(ctx, duration, metric.WithAttributes(m.getOtelAttributes()...))
}

// --- Stellar-specific metrics ---

func (m *stellarTxmMetrics) IncrementRestore(ctx context.Context, outcome RestoreOutcome) {
	promStellarTxmRestore.WithLabelValues(m.chainID, string(outcome)).Add(1)
	otelAttrs := append(
		m.getOtelAttributes(),
		attribute.String("outcome", string(outcome)),
	)
	m.restore.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) ObserveSimulationDuration(ctx context.Context, milliseconds int64) {
	promStellarTxmSimDuration.WithLabelValues(m.chainID).Observe(float64(milliseconds))
	m.simDuration.Record(ctx, milliseconds, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveInclusionFee(ctx context.Context, stroops int64) {
	promStellarTxmFeeInclusion.WithLabelValues(m.chainID).Observe(float64(stroops))
	m.feeInclusion.Record(ctx, stroops, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveResourceFee(ctx context.Context, stroops int64) {
	promStellarTxmFeeResource.WithLabelValues(m.chainID).Observe(float64(stroops))
	m.feeResource.Record(ctx, stroops, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveRPCHeadLag(ctx context.Context, ledgers int64) {
	m.rpcHeadLag.Record(ctx, ledgers, metric.WithAttributes(m.getOtelAttributes()...))
}

type noopStellarTxmMetrics struct{}

func (noopStellarTxmMetrics) IncrementBroadcastedTxs(context.Context, uint64) {}

func (noopStellarTxmMetrics) IncrementSuccessTxs(context.Context, uint64) {}

func (noopStellarTxmMetrics) SetPendingTxs(context.Context, int) {}

func (noopStellarTxmMetrics) IncrementErrorTxs(context.Context, ErrorReason) {}

func (noopStellarTxmMetrics) IncrementRetryTxs(context.Context, RetryReason) {}

func (noopStellarTxmMetrics) IncrementDroppedTxs(context.Context, DropReason) {}

func (noopStellarTxmMetrics) IncrementMaxAttemptsReached(context.Context, RetryBudget) {}

func (noopStellarTxmMetrics) IncrementSubmitRejected(context.Context, ErrorReason) {}

func (noopStellarTxmMetrics) IncrementRestore(context.Context, RestoreOutcome) {}

func (noopStellarTxmMetrics) ObserveSimulationDuration(context.Context, int64) {}

func (noopStellarTxmMetrics) ObserveInclusionFee(context.Context, int64) {}

func (noopStellarTxmMetrics) ObserveResourceFee(context.Context, int64) {}

func (noopStellarTxmMetrics) ObserveRPCHeadLag(context.Context, int64) {}

func (noopStellarTxmMetrics) RecordTimeUntilTxConfirmed(context.Context, float64) {}

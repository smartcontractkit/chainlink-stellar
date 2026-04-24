package txm

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/smartcontractkit/chainlink-common/pkg/beholder"
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

	promStellarTxmFinalizedTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_finalized",
		Help: "Number of transactions reaching Finalized status",
	}, []string{"chainID"})

	promStellarTxmPendingTxs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stellar_txm_tx_pending",
		Help: "Current in-flight unconfirmed transactions",
	}, []string{"chainID"})

	promStellarTxmErrorTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_error",
		Help: "Transaction errors by reason",
	}, []string{"chainID", "reason"})

	promStellarTxmRetryTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_retry",
		Help: "Transaction retries by reason",
	}, []string{"chainID", "reason"})

	promStellarTxmDroppedTxs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_tx_dropped",
		Help: "Transactions dropped due to backpressure (oldest-evicted or new-rejected)",
	}, []string{"chainID", "reason"})

	// Stellar-specific metrics

	promStellarTxmRestoreTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_restore_total",
		Help: "RestoreFootprint transactions submitted",
	}, []string{"chainID"})

	promStellarTxmRestoreSuccess = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_restore_success",
		Help: "RestoreFootprint transactions that succeeded",
	}, []string{"chainID"})

	promStellarTxmRestoreFailed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_restore_failed",
		Help: "RestoreFootprint transactions that failed",
	}, []string{"chainID"})

	promStellarTxmSimDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_simulation_duration_seconds",
		Help:    "Time spent in SimulateTransaction calls",
		Buckets: prometheus.DefBuckets,
	}, []string{"chainID"})

	promStellarTxmFeeInclusion = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_fee_inclusion_stroops",
		Help:    "Inclusion fee paid (in stroops)",
		Buckets: []float64{100, 200, 500, 1000, 5000, 10000, 50000, 100000},
	}, []string{"chainID"})

	promStellarTxmFeeResource = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "stellar_txm_fee_resource_stroops",
		Help:    "Resource fee charged (in stroops)",
		Buckets: []float64{10000, 50000, 100000, 500000, 1000000, 5000000},
	}, []string{"chainID"})
)

type stellarTxmMetrics struct {
	metrics.Labeler
	chainID string

	// Shared metrics (all chain TXMs have these)
	broadcastedTxs metric.Int64Counter
	successTxs     metric.Int64Counter
	finalizedTxs   metric.Int64Counter
	pendingTxs     metric.Int64Gauge
	errorTxs       metric.Int64Counter
	retryTxs       metric.Int64Counter
	droppedTxs     metric.Int64Counter

	// Stellar-specific metrics
	restoreTotal   metric.Int64Counter
	restoreSuccess metric.Int64Counter
	restoreFailed  metric.Int64Counter
	simDuration    metric.Float64Histogram
	feeInclusion   metric.Int64Histogram
	feeResource    metric.Int64Histogram
}

func newStellarTxmMetrics(chainID string) (*stellarTxmMetrics, error) {
	m := beholder.GetMeter()

	broadcastedTxs, err := m.Int64Counter("stellar_txm_tx_broadcasted")
	if err != nil {
		return nil, fmt.Errorf("failed to register broadcasted txs counter: %w", err)
	}

	successTxs, err := m.Int64Counter("stellar_txm_tx_success")
	if err != nil {
		return nil, fmt.Errorf("failed to register success txs counter: %w", err)
	}

	finalizedTxs, err := m.Int64Counter("stellar_txm_tx_finalized")
	if err != nil {
		return nil, fmt.Errorf("failed to register finalized txs counter: %w", err)
	}

	pendingTxs, err := m.Int64Gauge("stellar_txm_tx_pending")
	if err != nil {
		return nil, fmt.Errorf("failed to register pending txs gauge: %w", err)
	}

	errorTxs, err := m.Int64Counter("stellar_txm_tx_error")
	if err != nil {
		return nil, fmt.Errorf("failed to register error txs counter: %w", err)
	}

	retryTxs, err := m.Int64Counter("stellar_txm_tx_retry")
	if err != nil {
		return nil, fmt.Errorf("failed to register retry txs counter: %w", err)
	}

	droppedTxs, err := m.Int64Counter("stellar_txm_tx_dropped")
	if err != nil {
		return nil, fmt.Errorf("failed to register dropped txs counter: %w", err)
	}

	restoreTotal, err := m.Int64Counter("stellar_txm_restore_total")
	if err != nil {
		return nil, fmt.Errorf("failed to register restore total counter: %w", err)
	}

	restoreSuccess, err := m.Int64Counter("stellar_txm_restore_success")
	if err != nil {
		return nil, fmt.Errorf("failed to register restore success counter: %w", err)
	}

	restoreFailed, err := m.Int64Counter("stellar_txm_restore_failed")
	if err != nil {
		return nil, fmt.Errorf("failed to register restore failed counter: %w", err)
	}

	simDuration, err := m.Float64Histogram("stellar_txm_simulation_duration_seconds")
	if err != nil {
		return nil, fmt.Errorf("failed to register simulation duration histogram: %w", err)
	}

	feeInclusion, err := m.Int64Histogram("stellar_txm_fee_inclusion_stroops")
	if err != nil {
		return nil, fmt.Errorf("failed to register fee inclusion histogram: %w", err)
	}

	feeResource, err := m.Int64Histogram("stellar_txm_fee_resource_stroops")
	if err != nil {
		return nil, fmt.Errorf("failed to register fee resource histogram: %w", err)
	}

	return &stellarTxmMetrics{
		chainID: chainID,
		Labeler: metrics.NewLabeler().With("chainID", chainID),

		broadcastedTxs: broadcastedTxs,
		successTxs:     successTxs,
		finalizedTxs:   finalizedTxs,
		pendingTxs:     pendingTxs,
		errorTxs:       errorTxs,
		retryTxs:       retryTxs,
		droppedTxs:     droppedTxs,

		restoreTotal:   restoreTotal,
		restoreSuccess: restoreSuccess,
		restoreFailed:  restoreFailed,
		simDuration:    simDuration,
		feeInclusion:   feeInclusion,
		feeResource:    feeResource,
	}, nil
}

func (m *stellarTxmMetrics) getOtelAttributes() []attribute.KeyValue {
	return beholder.OtelAttributes(m.Labels).AsStringAttributes()
}

// --- Shared metrics (ported from Aptos) ---

func (m *stellarTxmMetrics) IncrementBroadcastedTxs(ctx context.Context) {
	promStellarTxmBroadcastedTxs.WithLabelValues(m.chainID).Add(1)
	m.broadcastedTxs.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementSuccessTxs(ctx context.Context) {
	promStellarTxmSuccessTxs.WithLabelValues(m.chainID).Add(1)
	m.successTxs.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementFinalizedTxs(ctx context.Context) {
	promStellarTxmFinalizedTxs.WithLabelValues(m.chainID).Add(1)
	m.finalizedTxs.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) SetPendingTxs(ctx context.Context, count int) {
	promStellarTxmPendingTxs.WithLabelValues(m.chainID).Set(float64(count))
	m.pendingTxs.Record(ctx, int64(count), metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementErrorTxs(ctx context.Context, reason string) {
	promStellarTxmErrorTxs.WithLabelValues(m.chainID, reason).Add(1)
	otelAttrs := append(m.getOtelAttributes(), attribute.String("reason", reason))
	m.errorTxs.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) IncrementRetryTxs(ctx context.Context, reason string) {
	promStellarTxmRetryTxs.WithLabelValues(m.chainID, reason).Add(1)
	otelAttrs := append(m.getOtelAttributes(), attribute.String("reason", reason))
	m.retryTxs.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

func (m *stellarTxmMetrics) IncrementDroppedTxs(ctx context.Context, reason string) {
	promStellarTxmDroppedTxs.WithLabelValues(m.chainID, reason).Add(1)
	otelAttrs := append(m.getOtelAttributes(), attribute.String("reason", reason))
	m.droppedTxs.Add(ctx, 1, metric.WithAttributes(otelAttrs...))
}

// --- Stellar-specific metrics ---

func (m *stellarTxmMetrics) IncrementRestoreTotal(ctx context.Context) {
	promStellarTxmRestoreTotal.WithLabelValues(m.chainID).Add(1)
	m.restoreTotal.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementRestoreSuccess(ctx context.Context) {
	promStellarTxmRestoreSuccess.WithLabelValues(m.chainID).Add(1)
	m.restoreSuccess.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) IncrementRestoreFailed(ctx context.Context) {
	promStellarTxmRestoreFailed.WithLabelValues(m.chainID).Add(1)
	m.restoreFailed.Add(ctx, 1, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveSimulationDuration(ctx context.Context, seconds float64) {
	promStellarTxmSimDuration.WithLabelValues(m.chainID).Observe(seconds)
	m.simDuration.Record(ctx, seconds, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveInclusionFee(ctx context.Context, stroops int64) {
	promStellarTxmFeeInclusion.WithLabelValues(m.chainID).Observe(float64(stroops))
	m.feeInclusion.Record(ctx, stroops, metric.WithAttributes(m.getOtelAttributes()...))
}

func (m *stellarTxmMetrics) ObserveResourceFee(ctx context.Context, stroops int64) {
	promStellarTxmFeeResource.WithLabelValues(m.chainID).Observe(float64(stroops))
	m.feeResource.Record(ctx, stroops, metric.WithAttributes(m.getOtelAttributes()...))
}

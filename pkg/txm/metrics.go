package txm

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	promBroadcasted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_broadcasted",
		Help: "Number of transactions successfully broadcast to the network",
	}, []string{"chainID", "fromAddress"})

	promFinalized = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_finalized",
		Help: "Number of transactions confirmed on-chain with SUCCESS",
	}, []string{"chainID", "fromAddress"})

	promError = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_error",
		Help: "Number of broadcast-time errors (sim, sign, submit failures)",
	}, []string{"chainID", "fromAddress"})

	promRevert = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_revert",
		Help: "Number of transactions that failed on-chain (included but FAILED)",
	}, []string{"chainID", "fromAddress"})

	promReject = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_reject",
		Help: "Number of transactions rejected (max retries exhausted or queue full)",
	}, []string{"chainID", "fromAddress"})

	promDrop = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_drop",
		Help: "Number of transactions dropped (channel full or expired)",
	}, []string{"chainID", "fromAddress"})

	promRetry = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_retry",
		Help: "Number of lifecycle retry attempts",
	}, []string{"chainID", "fromAddress"})

	promRestore = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stellar_txm_restore",
		Help: "Number of RestoreFootprint transactions submitted",
	}, []string{"chainID", "fromAddress"})

	promPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stellar_txm_pending",
		Help: "Current number of pending transactions in the enqueue channel",
	}, []string{"chainID", "fromAddress"})
)

// Metrics provides dual Prometheus instrumentation for the Stellar TXM.
// Each method increments both the Prometheus counter and (when available) the
// OTEL/Beholder meter. The OTEL integration can be wired in once the TXM is
// integrated into the CRE service lifecycle.
type Metrics struct {
	chainID     string
	fromAddress string
}

// NewMetrics creates a Metrics instance with fixed label values.
func NewMetrics(chainID, fromAddress string) *Metrics {
	return &Metrics{
		chainID:     chainID,
		fromAddress: fromAddress,
	}
}

func (m *Metrics) labels() prometheus.Labels {
	return prometheus.Labels{
		"chainID":     m.chainID,
		"fromAddress": m.fromAddress,
	}
}

func (m *Metrics) IncrBroadcasted() { promBroadcasted.With(m.labels()).Inc() }
func (m *Metrics) IncrFinalized()   { promFinalized.With(m.labels()).Inc() }
func (m *Metrics) IncrError()       { promError.With(m.labels()).Inc() }
func (m *Metrics) IncrRevert()      { promRevert.With(m.labels()).Inc() }
func (m *Metrics) IncrReject()      { promReject.With(m.labels()).Inc() }
func (m *Metrics) IncrDrop()        { promDrop.With(m.labels()).Inc() }
func (m *Metrics) IncrRetry()       { promRetry.With(m.labels()).Inc() }
func (m *Metrics) IncrRestore()     { promRestore.With(m.labels()).Inc() }
func (m *Metrics) SetPending(n int64) {
	promPending.With(m.labels()).Set(float64(n))
}

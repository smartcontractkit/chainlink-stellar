package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/config/configtest"
)

// TestDefaults_fieldsNotNil catches docs.toml drift: a field in the struct but
// missing from docs.toml leaves a nil pointer after Defaults().
func TestDefaults_fieldsNotNil(t *testing.T) {
	configtest.AssertFieldsNotNil(t, Defaults())
}

// TestDocsTOMLComplete catches struct drift: a field in TOMLConfig but missing
// from docs.toml fails this test.
func TestDocsTOMLComplete(t *testing.T) {
	configtest.AssertDocsTOMLComplete[TOMLConfig](t, docsTOML)
}

func TestGenerateDocs(t *testing.T) {
	out, err := GenerateDocs()
	require.NoError(t, err)
	require.NotEmpty(t, out)
}

// TestDefaultConfigSet_MatchesDocsTOML enforces that DefaultConfigSet (used by
// Resolve) and docs.toml (used by Defaults/SetDefaults) stay in sync. Without
// this, the two default sources can silently drift.
func TestDefaultConfigSet_MatchesDocsTOML(t *testing.T) {
	d := Defaults().TxManager
	require.Equal(t, *DefaultConfigSet.BroadcastChanSize, *d.BroadcastChanSize)
	require.Equal(t, *DefaultConfigSet.ConfirmPollInterval, *d.ConfirmPollInterval)
	require.Equal(t, *DefaultConfigSet.BaseInclusionFee, *d.BaseInclusionFee)
	require.Equal(t, *DefaultConfigSet.MaxInclusionFee, *d.MaxInclusionFee)
	require.Equal(t, *DefaultConfigSet.FeeBumpMultiplier, *d.FeeBumpMultiplier)
	require.Equal(t, *DefaultConfigSet.ResourceFeeBuffer, *d.ResourceFeeBuffer)
	require.Equal(t, *DefaultConfigSet.RestoreFeeBuffer, *d.RestoreFeeBuffer)
	require.Equal(t, *DefaultConfigSet.FeeStatsPollInterval, *d.FeeStatsPollInterval)
	require.Equal(t, *DefaultConfigSet.MaxSimulateAttempts, *d.MaxSimulateAttempts)
	require.Equal(t, *DefaultConfigSet.MaxSubmitRetryAttempts, *d.MaxSubmitRetryAttempts)
	require.Equal(t, *DefaultConfigSet.SubmitRetryDelay, *d.SubmitRetryDelay)
	require.Equal(t, *DefaultConfigSet.TxTimeoutSecs, *d.TxTimeoutSecs)
	require.Equal(t, *DefaultConfigSet.LedgerBoundsOffset, *d.LedgerBoundsOffset)
	require.Equal(t, *DefaultConfigSet.MaxTxRetryAttempts, *d.MaxTxRetryAttempts)
	require.Equal(t, *DefaultConfigSet.MaxGetClientRetryAttempts, *d.MaxGetClientRetryAttempts)
	require.Equal(t, *DefaultConfigSet.MaxRestoreAttempts, *d.MaxRestoreAttempts)
	require.Equal(t, *DefaultConfigSet.PruneInterval, *d.PruneInterval)
	require.Equal(t, *DefaultConfigSet.PruneTxExpiration, *d.PruneTxExpiration)
}

// TestDefaults_CopyIsolation ensures SetFrom deep-copies pointer fields so
// mutating a Defaults() copy cannot corrupt the package-level defaults.
func TestDefaults_CopyIsolation(t *testing.T) {
	d1 := Defaults()
	original := *d1.TxManager.BroadcastChanSize
	*d1.TxManager.BroadcastChanSize = 999
	d2 := Defaults()
	require.Equal(t, original, *d2.TxManager.BroadcastChanSize,
		"mutating a Defaults() copy must not corrupt the package-level defaults")
}

package txm

import (
	"time"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
)

// GetContextedTxLogger returns a logger with transaction context fields attached.
func GetContextedTxLogger(lgr logger.Logger, txID string, meta *commontypes.TxMeta) logger.Logger {
	fields := []interface{}{"txID", txID}
	if meta != nil {
		if meta.WorkflowExecutionID != nil {
			fields = append(fields, "workflowExecutionID", *meta.WorkflowExecutionID)
		}
	}
	return logger.With(lgr, fields...)
}

func getTimestampSecs() uint64 {
	return uint64(time.Now().Unix())
}

package deployment

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// debugStellarLogPath is the Cursor debug-mode NDJSON log for session a4ab59.
const debugStellarLogPath = "/Users/felix/dev/chainlink-stellar/.cursor/debug-a4ab59.log"

// DebugSessionLogStellar appends one NDJSON line (session a4ab59) for hypothesis testing.
// Safe to call on failure paths; ignores I/O errors.
func DebugSessionLogStellar(hypothesisID, location, message string, data map[string]any) {
	payload := map[string]any{
		"sessionId":     "a4ab59",
		"hypothesisId":  hypothesisID,
		"location":      location,
		"message":       message,
		"timestamp":     time.Now().UnixMilli(),
		"budgetKeyword": strings.Contains(strings.ToLower(message), "budget") || strings.Contains(strings.ToLower(message), "exceededlimit"),
	}
	for k, v := range data {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	f, err := os.OpenFile(debugStellarLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

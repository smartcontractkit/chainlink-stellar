package cre

import (
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

func TestParseReportProcessedEvent_LiveWireFormat(t *testing.T) {
	e := protocolrpc.EventInfo{
		Ledger:          53,
		TransactionHash: "8b4747298cf5c8f2c0b2ff394e6e6a3f2f2d31f9f3f1c2ab9d9d1d3d4e5f6a7b",
		TopicXDR: []string{
			"AAAADwAAABlmb3J3YXJkZXJfUmVwb3J0UHJvY2Vzc2VkAAAA",
			"AAAAEgAAAAFwyJcYECnxZoGE7nRR58Ft2OhUea/G8Y0QSI0BztOb0g==",
			"AAAADQAAACB3rRSnnTR3baSLxj4SK8qnH6oK4kh4JYqF62/EA28tNw==",
			"AAAADQAAAAIAAQAA",
		},
		ValueXDR: "AAAAAAAAAAE=",
	}

	parsed, err := ParseReportProcessedEvent(e)
	if err != nil {
		t.Fatalf("ParseReportProcessedEvent: %v", err)
	}
	if !parsed.Success {
		t.Error("Success = false, want true")
	}
	wantExec := [32]byte{0x77, 0xad, 0x14, 0xa7, 0x9d, 0x34, 0x77, 0x6d, 0xa4, 0x8b, 0xc6, 0x3e, 0x12, 0x2b, 0xca, 0xa7, 0x1f, 0xaa, 0x0a, 0xe2, 0x48, 0x78, 0x25, 0x8a, 0x85, 0xeb, 0x6f, 0xc4, 0x03, 0x6f, 0x2d, 0x37}
	if parsed.WorkflowExecutionId != wantExec {
		t.Errorf("WorkflowExecutionId = %x, want %x", parsed.WorkflowExecutionId, wantExec)
	}
	if parsed.ReportId != [2]byte{0x00, 0x01} {
		t.Errorf("ReportId = %x, want 0001", parsed.ReportId)
	}
	if parsed.Receiver == "" {
		t.Error("Receiver not parsed from topic")
	}
	if parsed.Ledger != 53 {
		t.Errorf("Ledger = %d, want 53", parsed.Ledger)
	}
}

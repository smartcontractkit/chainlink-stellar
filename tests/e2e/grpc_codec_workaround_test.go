package e2e_tests

import (
	"fmt"

	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

// init works around open-telemetry/opentelemetry-collector#14088.
//
// go.opentelemetry.io/collector/pdata@v1.59.0 registers a custom gRPC CodecV2
// under the name "proto" (internal/otelgrpc/encoding.go) that wraps the default
// proto codec. It captures that delegate in its own init() via
// encoding.GetCodecV2("proto"); when google.golang.org/grpc/encoding/proto's
// init() has not run yet — and the init order between the two is unspecified
// (see golang/go#57411) — the delegate is nil. The first gRPC call that then
// marshals a non-OTLP message (e.g. the CCV verifier
// GetVerifierResultsForMessage request issued from AssertMessage) panics with a
// nil-pointer dereference inside codecV2.Marshal.
//
// A package's init() is guaranteed to run after all of its imports' init()s,
// so registering a working proto codec here overwrites the buggy one regardless
// of the unspecified order between otelgrpc and encoding/proto. The codec below
// mirrors google.golang.org/grpc/encoding/proto's codecV2; the only behaviour
// lost is pdata's buffer-pool optimisation for OTLP payloads, which these e2e
// tests do not exercise.
//
// This was introduced by the "bump dependencies" commit that pulled in
// collector/pdata v1.59.0 alongside grpc v1.83.2; it is unrelated to any
// contract change. Remove once a pdata release ships that no longer captures a
// nil delegate (see upstream issue for the fixing version).
func init() {
	encoding.RegisterCodecV2(&protoCodecV2{})
}

type protoCodecV2 struct{}

func (protoCodecV2) Name() string { return "proto" }

func (protoCodecV2) Marshal(v any) (mem.BufferSlice, error) {
	vv := messageV2Of(v)
	if vv == nil {
		return nil, fmt.Errorf("proto: failed to marshal, message is %T, want proto.Message", v)
	}
	b, err := proto.Marshal(vv)
	if err != nil {
		return nil, err
	}
	return mem.BufferSlice{mem.SliceBuffer(b)}, nil
}

func (protoCodecV2) Unmarshal(data mem.BufferSlice, v any) error {
	vv := messageV2Of(v)
	if vv == nil {
		return fmt.Errorf("proto: failed to unmarshal, message is %T, want proto.Message", v)
	}
	return proto.Unmarshal(data.Materialize(), vv)
}

func messageV2Of(v any) proto.Message {
	switch v := v.(type) {
	case protoadapt.MessageV1:
		return protoadapt.MessageV2Of(v)
	case protoadapt.MessageV2:
		return v
	}
	return nil
}

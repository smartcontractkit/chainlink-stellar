package data_feeds_cache

import (
	"reflect"
	"testing"
)

// Every contract function (minus __constructor) must have a generated client
// method. Expected list derived from the contract interface files in
// contracts/common/interfaces/src/ (every exported fn minus __constructor).
func TestClientCompleteness(t *testing.T) {
	expected := []string{
		"Upgrade", "Version", "Decimals", "GetOwner", "GetRound", "OnReport",
		"FindRound", "Description", "RoundRange", "LatestRound", "IsFeedAdmin",
		"AddFeedAdmin", "HasPermission", "RecoverTokens", "AcceptOwnership",
		"SetFeedConfigs", "TypeAndVersion", "RemoveFeedAdmin", "RenounceOwnership",
		"TransferOwnership", "RemoveFeedConfigs", "GetFeedPermissions",
	}
	ct := reflect.TypeOf(&DataFeedsCacheClient{})
	for _, name := range expected {
		if _, ok := ct.MethodByName(name); !ok {
			t.Errorf("generated client is missing method %s", name)
		}
	}
}

// Every #[contractevent(...)] struct in the interface file must have a
// generated event type and a Parse<Name> helper. Expected list derived from
// the contract interface files in contracts/common/interfaces/src/ (every
// #[contractevent] struct; 12 in data_feeds_cache.rs).
//
// Referencing each type and Parse func by name below is itself a compile-time
// completeness check: if the generator ever silently drops an event again (as
// it did before parseEvents was made attribute-arg-order-independent — see
// generator/parser.go), this file fails to build rather than a test failing
// at runtime.
func TestEventParseCompleteness(t *testing.T) {
	expectedEvents := []string{
		"StaleReport", "FeedConfigSet", "FeedUpdated", "FeedAdminAdded",
		"FeedAdminRemoved", "FeedConfigRemoved", "InvalidUpdatePermission",
		"Upgraded", "TokenRecovered", "OwnershipTransfer", "OwnershipRenounced",
		"OwnershipTransferCompleted",
	}

	eventTypes := map[string]reflect.Type{
		"StaleReport":                reflect.TypeOf(StaleReport{}),
		"FeedConfigSet":              reflect.TypeOf(FeedConfigSet{}),
		"FeedUpdated":                reflect.TypeOf(FeedUpdated{}),
		"FeedAdminAdded":             reflect.TypeOf(FeedAdminAdded{}),
		"FeedAdminRemoved":           reflect.TypeOf(FeedAdminRemoved{}),
		"FeedConfigRemoved":          reflect.TypeOf(FeedConfigRemoved{}),
		"InvalidUpdatePermission":    reflect.TypeOf(InvalidUpdatePermission{}),
		"Upgraded":                   reflect.TypeOf(Upgraded{}),
		"TokenRecovered":             reflect.TypeOf(TokenRecovered{}),
		"OwnershipTransfer":          reflect.TypeOf(OwnershipTransfer{}),
		"OwnershipRenounced":         reflect.TypeOf(OwnershipRenounced{}),
		"OwnershipTransferCompleted": reflect.TypeOf(OwnershipTransferCompleted{}),
	}

	parseFuncs := map[string]interface{}{
		"StaleReport":                ParseStaleReport,
		"FeedConfigSet":              ParseFeedConfigSet,
		"FeedUpdated":                ParseFeedUpdated,
		"FeedAdminAdded":             ParseFeedAdminAdded,
		"FeedAdminRemoved":           ParseFeedAdminRemoved,
		"FeedConfigRemoved":          ParseFeedConfigRemoved,
		"InvalidUpdatePermission":    ParseInvalidUpdatePermission,
		"Upgraded":                   ParseUpgraded,
		"TokenRecovered":             ParseTokenRecovered,
		"OwnershipTransfer":          ParseOwnershipTransfer,
		"OwnershipRenounced":         ParseOwnershipRenounced,
		"OwnershipTransferCompleted": ParseOwnershipTransferCompleted,
	}

	if len(eventTypes) != len(expectedEvents) {
		t.Fatalf("eventTypes map has %d entries, expected %d", len(eventTypes), len(expectedEvents))
	}
	if len(parseFuncs) != len(expectedEvents) {
		t.Fatalf("parseFuncs map has %d entries, expected %d", len(parseFuncs), len(expectedEvents))
	}

	for _, name := range expectedEvents {
		if _, ok := eventTypes[name]; !ok {
			t.Errorf("generated bindings missing event type %s", name)
		}
		if _, ok := parseFuncs[name]; !ok {
			t.Errorf("generated bindings missing Parse%s helper", name)
		}
	}

	// FeedUpdated.Answer must decode Soroban I256 as *big.Int (scval.I256FromScVal),
	// not be silently dropped or mistyped.
	if f, ok := eventTypes["FeedUpdated"].FieldByName("Answer"); !ok {
		t.Error("FeedUpdated missing Answer field")
	} else if f.Type.String() != "*big.Int" {
		t.Errorf("FeedUpdated.Answer type = %s, want *big.Int", f.Type.String())
	}
}

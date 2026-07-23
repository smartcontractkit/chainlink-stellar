package data_feeds_proxy

import (
	"reflect"
	"testing"
)

// Every contract function (minus __constructor) must have a generated client
// method. Expected list derived from the contract interface files in
// contracts/common/interfaces/src/ (every exported fn minus __constructor).
func TestClientCompleteness(t *testing.T) {
	expected := []string{
		"Upgrade", "Version", "Decimals", "GetOwner", "GetRound", "SetCache",
		"Description", "LatestRound", "RecoverTokens", "AcceptOwnership",
		"TypeAndVersion", "RenounceOwnership", "TransferOwnership",
	}
	ct := reflect.TypeOf(&DataFeedsProxyClient{})
	for _, name := range expected {
		if _, ok := ct.MethodByName(name); !ok {
			t.Errorf("generated client is missing method %s", name)
		}
	}
}

// Every #[contractevent(...)] struct in the interface file must have a
// generated event type and a Parse<Name> helper (6 in data_feeds_proxy.rs).
// Referencing each type and Parse func by name is the real, compile-time
// completeness check: if the generator ever silently drops an event again (as
// it did before parseEvents was made attribute-arg-order-independent — see
// generator/parser.go), this file fails to build rather than a test failing
// at runtime. The count pin below guards against rows being deleted here.
var proxyEvents = []struct {
	typ   reflect.Type
	parse any
}{
	{reflect.TypeOf(CacheSet{}), ParseCacheSet},
	{reflect.TypeOf(Upgraded{}), ParseUpgraded},
	{reflect.TypeOf(TokenRecovered{}), ParseTokenRecovered},
	{reflect.TypeOf(OwnershipTransfer{}), ParseOwnershipTransfer},
	{reflect.TypeOf(OwnershipRenounced{}), ParseOwnershipRenounced},
	{reflect.TypeOf(OwnershipTransferCompleted{}), ParseOwnershipTransferCompleted},
}

func TestEventParseCompleteness(t *testing.T) {
	if got, want := len(proxyEvents), 6; got != want {
		t.Fatalf("proxyEvents pins %d events, expected %d", got, want)
	}
}

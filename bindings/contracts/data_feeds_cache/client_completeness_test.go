package data_feeds_cache

import (
	"math/big"
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
// generated event type and a Parse<Name> helper (12 in data_feeds_cache.rs).
// Referencing each type and Parse func by name is the real, compile-time
// completeness check: if the generator ever silently drops an event again (as
// it did before parseEvents was made attribute-arg-order-independent — see
// generator/parser.go), this file fails to build rather than a test failing
// at runtime. The count pin below guards against rows being deleted here.
var cacheEvents = []struct {
	typ   reflect.Type
	parse any
}{
	{reflect.TypeOf(StaleReport{}), ParseStaleReport},
	{reflect.TypeOf(FeedConfigSet{}), ParseFeedConfigSet},
	{reflect.TypeOf(FeedUpdated{}), ParseFeedUpdated},
	{reflect.TypeOf(FeedAdminAdded{}), ParseFeedAdminAdded},
	{reflect.TypeOf(FeedAdminRemoved{}), ParseFeedAdminRemoved},
	{reflect.TypeOf(FeedConfigRemoved{}), ParseFeedConfigRemoved},
	{reflect.TypeOf(InvalidUpdatePermission{}), ParseInvalidUpdatePermission},
	{reflect.TypeOf(Upgraded{}), ParseUpgraded},
	{reflect.TypeOf(TokenRecovered{}), ParseTokenRecovered},
	{reflect.TypeOf(OwnershipTransfer{}), ParseOwnershipTransfer},
	{reflect.TypeOf(OwnershipRenounced{}), ParseOwnershipRenounced},
	{reflect.TypeOf(OwnershipTransferCompleted{}), ParseOwnershipTransferCompleted},
}

// FeedUpdated.Answer must decode Soroban I256 as *big.Int (scval.I256FromScVal),
// not be silently dropped or mistyped. Compile-time check.
var _ *big.Int = FeedUpdated{}.Answer

func TestEventParseCompleteness(t *testing.T) {
	if got, want := len(cacheEvents), 12; got != want {
		t.Fatalf("cacheEvents pins %d events, expected %d", got, want)
	}
}

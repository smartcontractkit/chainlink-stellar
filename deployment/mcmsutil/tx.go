package mcmsutil

import (
	"encoding/json"
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
)

// MCMSInvokeTx wraps an encoded Soroban invoke payload as a one-transaction MCMS
// batch operation against ref. ref.Address must already be the contract strkey.
func MCMSInvokeTx(ref datastore.AddressRef, data []byte) mcmstypes.BatchOperation {
	return mcmstypes.BatchOperation{
		ChainSelector: mcmstypes.ChainSelector(ref.ChainSelector),
		Transactions: []mcmstypes.Transaction{{
			OperationMetadata: mcmstypes.OperationMetadata{
				ContractType: string(ref.Type),
			},
			To:               ref.Address,
			Data:             data,
			AdditionalFields: StellarMCMSTxAdditionalFields(),
		}},
	}
}

// StellarMCMSTxAdditionalFields marks proposal transactions as Soroban invokes so
// the MCMS transformer decodes them with the matching encoding version.
func StellarMCMSTxAdditionalFields() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"family":"stellar","encodingVersion":%d}`, bindings.SorobanInvokeEncodingVersion))
}

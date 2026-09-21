package common

import "github.com/smartcontractkit/chainlink-stellar/deployment"

const (
	StellarDeployerKeypairEnv   = "STELLAR_DEPLOYER_PRIVATE_KEY"
	StellarCCIPMessageSentTopic = "onramp_1_7_CCIPMessageSent"
)

// StellarTransmitterKeyName re-exports deployment.StellarTransmitterKeyName.
//
// The canonical definition lives in the deployment package so that
// deployment/adapters can reference it without importing ccv/common (which
// would recreate the old ccv <-> deployment cycle). It is re-exported here so
// the cmd binaries (committee-verifier, executor) and the accessor can reference
// it through the ccv module, keeping the root module from taking a direct
// dependency on deployment for a single constant.
const StellarTransmitterKeyName = deployment.StellarTransmitterKeyName

package deployment

import selectors "github.com/smartcontractkit/chain-selectors"

// StellarTransmitterKeyName is the full keystore path of the Ed25519 key used by
// the Stellar accessor as the transmitter / deployer keypair when signing Soroban
// transactions. The "stellar/tx/" prefix mirrors the "evm/tx/" convention used by
// chainlink-ccv's executor.DefaultEVMTransmitterKeyName.
//
// It is declared here (in the deployment module) rather than in ccv/common to avoid
// a ccv <-> deployment import cycle: ccv already imports deployment for the deployer
// primitives (TxSigner, NewDeployerWithSigner), and deployment/adapters needs this
// key name when building executor chain configs, so keeping it on the deployment side
// holds the edge one-way (ccv -> deployment) with no back-edge.
const StellarTransmitterKeyName = selectors.FamilyStellar + "/tx/stellar_transmitter_ed25519_key"

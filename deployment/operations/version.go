package operations

import "github.com/Masterminds/semver/v3"

// ContractDeploymentVersion is the semver stamped on Stellar Soroban deployment
// operation reports. Bump it when deploy behavior changes so older reports never
// replay.
var ContractDeploymentVersion = semver.MustParse("3.0.0")

// ContractRefVersion is the version on datastore address refs and adapter
// registry keys. It tracks the contract API, so report bumps leave it unchanged.
var ContractRefVersion = semver.MustParse("2.0.0")

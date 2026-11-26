package sequences

import "github.com/Masterminds/semver/v3"

// SequenceVersion is the semver for Stellar CCIP deployment sequences aligned
// with deployment/v2_0_0 adapters.
var SequenceVersion = semver.MustParse("3.1.0")

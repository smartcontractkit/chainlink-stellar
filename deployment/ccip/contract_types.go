package ccip

// Datastore contract type strings for Stellar CCIP deployments.
const (
	CcipReceiverContractType               = "CcipReceiverExample"
	TokenAdminRegistryContractType         = "token_admin_registry"
	LockReleaseTokenPoolContractType       = "lock_release_token_pool"
	SiloedLockReleaseTokenPoolContractType = "siloed_lock_release_token_pool"
	TokenLockBoxContractType               = "token_lock_box"
	TestTokenContractType                  = "sac_token"

	// DevenvTestTokenPoolQualifier is the datastore qualifier for the siloed test pool used in E2E transfers.
	DevenvTestTokenPoolQualifier = "TEST"

	// DevenvLegacyLockReleasePoolQualifier is the datastore qualifier for the legacy lock-release pool
	// deployed alongside the siloed pool (e.g. MCMS / lock-release-specific tests).
	DevenvLegacyLockReleasePoolQualifier = "LEGACY_LR"

	// DevenvBnmPoolQualifier is the datastore qualifier for the lane's stellar
	// BnM burn-mint pool (and its ccv-catalog mirror row).
	DevenvBnmPoolQualifier = "BnM"

	// DevenvCatalogBurnMintTokenPoolType mirrors
	// chainlink-ccv/build/devenv/common.BurnMintTokenPoolType. Duplicated locally
	// because this module stays free of the chainlink-ccv dependency
	DevenvCatalogBurnMintTokenPoolType = "BurnMintTokenPool"

	// DevenvCatalogLockReleaseTokenPoolType mirrors
	// chainlink-ccv/build/devenv/common.LockReleaseTokenPoolType. Duplicated
	// locally for the same reason.
	DevenvCatalogLockReleaseTokenPoolType = "LockReleaseTokenPool"
)

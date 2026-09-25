use common_error::CCIPError;
use common_helpers::validation::{assert_ccv_set_valid, Validatable};
use soroban_sdk::{contracttype, Address, Vec};

/// Per-remote-chain CCV configuration, mirroring EVM `AdvancedPoolHooks.CCVConfig`.
///
/// CCV requirements are split by direction: `outbound_*` lists govern messages
/// leaving for the remote chain; `inbound_*` lists govern messages arriving
/// from it. Each direction carries a base list (always required) and a
/// threshold list (required only when the transfer amount is at or above the
/// configured threshold amount, see `set_threshold_amount`).
///
/// `outbound_include_defaults` / `inbound_include_defaults` replace EVM's
/// `address(0)` sentinel: when true, the pool appends its lane-default CCVs on
/// top of the configured list (EVM expresses the same intent by placing
/// `address(0)` inside the array). Soroban `Address` has no zero form, so the
/// intent is carried as an explicit boolean instead.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVConfig {
    pub outbound_ccvs: Vec<Address>,
    pub threshold_outbound_ccvs: Vec<Address>,
    pub inbound_ccvs: Vec<Address>,
    pub threshold_inbound_ccvs: Vec<Address>,
    pub outbound_include_defaults: bool,
    pub inbound_include_defaults: bool,
}

/// Caller-facing shape for `apply_ccv_config_updates`, mirroring EVM
/// `AdvancedPoolHooks.CCVConfigArg`. One entry per remote chain selector.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CCVConfigArg {
    pub remote_chain_selector: u64,
    pub outbound_ccvs: Vec<Address>,
    pub threshold_outbound_ccvs: Vec<Address>,
    pub inbound_ccvs: Vec<Address>,
    pub threshold_inbound_ccvs: Vec<Address>,
    pub outbound_include_defaults: bool,
    pub inbound_include_defaults: bool,
}

impl Validatable for CCVConfigArg {
    /// Mirrors EVM `applyCCVConfigUpdates` validation:
    /// - within-list duplicates and base/threshold cross-overlap (EVM
    ///   `CCVConfigValidation._assertNoDuplicates` +
    ///   `_assertNoDuplicatedBetweenLists`) via `assert_ccv_set_valid`;
    /// - base CCVs must be specified whenever above-threshold CCVs are (EVM
    ///   `MustSpecifyUnderThresholdCCVsForThresholdCCVs`). EVM's `address(0)`
    ///   sentinel counts as a base entry, so a threshold list is allowed with an
    ///   empty base list as long as the defaults are included; `include_defaults`
    ///   is Stellar's `address(0)` analogue and counts the same way here.
    fn validate(&self) -> Result<(), CCIPError> {
        assert_ccv_set_valid(
            &self.outbound_ccvs,
            &self.threshold_outbound_ccvs,
            CCIPError::DuplicateCCVNotAllowed,
        )?;
        assert_ccv_set_valid(
            &self.inbound_ccvs,
            &self.threshold_inbound_ccvs,
            CCIPError::DuplicateCCVNotAllowed,
        )?;

        // EVM rejects only when the base list is empty AND no `address(0)` is
        // present (AdvancedPoolHooks.sol#L258). `include_defaults` stands in for
        // `address(0)`, so a non-empty threshold list requires a non-empty base
        // list OR `include_defaults`, not a non-empty base list alone.
        if !self.threshold_outbound_ccvs.is_empty()
            && self.outbound_ccvs.is_empty()
            && !self.outbound_include_defaults
        {
            return Err(CCIPError::InvalidConfig);
        }
        if !self.threshold_inbound_ccvs.is_empty()
            && self.inbound_ccvs.is_empty()
            && !self.inbound_include_defaults
        {
            return Err(CCIPError::InvalidConfig);
        }
        Ok(())
    }
}

impl CCVConfig {
    /// Empty config: no CCVs in either direction, defaults included. The value
    /// returned by `get_required_ccvs` for a chain with no stored config.
    pub fn empty(env: &soroban_sdk::Env) -> Self {
        Self {
            outbound_ccvs: Vec::new(env),
            threshold_outbound_ccvs: Vec::new(env),
            inbound_ccvs: Vec::new(env),
            threshold_inbound_ccvs: Vec::new(env),
            outbound_include_defaults: true,
            inbound_include_defaults: true,
        }
    }
}

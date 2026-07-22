use soroban_sdk::{
    contract, contractimpl, symbol_short,
    testutils::{
        storage::Instance as _, Address as _, Events as _, Ledger as _, MockAuth, MockAuthInvoke,
    },
    xdr::ContractEvent,
    Address, Env, Event, IntoVal, String, Symbol, Val, Vec,
};
use stellar_access::ownable::{self, Ownable};

use crate::{TokenRecoverable, Upgradeable, Versioned, WasmHash};

extern crate alloc;

const SLOT: Symbol = symbol_short!("slot");

#[contract]
pub struct Mock;

#[contractimpl]
impl Mock {
    pub fn __constructor(env: Env, owner: Address) {
        ownable::set_owner(&env, &owner);
    }
    pub fn poke(env: Env, v: u32) {
        env.storage().instance().set(&SLOT, &v);
    }
}

#[contractimpl(contracttrait)]
impl Versioned for Mock {
    fn version(_env: Env) -> u32 {
        7
    }
    fn type_and_version(env: Env) -> String {
        String::from_str(&env, "Mock 7.0.0")
    }
}

#[contractimpl(contracttrait)]
impl Ownable for Mock {}

#[contractimpl(contracttrait)]
impl Upgradeable for Mock {}

#[contractimpl(contracttrait)]
impl TokenRecoverable for Mock {}

pub fn deploy() -> (Env, Address) {
    let env = Env::default();
    let owner = Address::generate(&env);
    let id = env.register(Mock, (owner,));
    (env, id)
}

pub fn execute_as_contract(f: impl FnOnce(&Env)) {
    let (env, id) = deploy();
    env.as_contract(&id, || f(&env));
}

pub fn new_address(env: &Env) -> Address {
    Address::generate(env)
}

pub fn roll(env: &Env, seq: u32) {
    env.ledger().with_mut(|li| li.sequence_number = seq);
}

pub fn set_network_max_ttl(env: &Env, ttl: u32) {
    env.ledger().set_max_entry_ttl(ttl);
}

pub fn network_max_ttl(env: &Env) -> u32 {
    env.ledger()
        .max_live_until_ledger()
        .saturating_sub(env.ledger().sequence())
}

pub fn age_ttl(env: &Env) {
    roll(env, network_max_ttl(env) / 2);
}

pub fn peek(env: &Env, id: &Address) -> u32 {
    env.invoke_contract(id, &symbol_short!("peek"), Vec::<Val>::new(env))
}

pub fn authorize<A: IntoVal<Env, Vec<Val>>>(
    env: &Env,
    id: &Address,
    user: &Address,
    fn_name: &'static str,
    args: A,
) {
    let invoke = MockAuthInvoke {
        contract: id,
        fn_name,
        args: args.into_val(env),
        sub_invokes: &[],
    };
    env.mock_auths(&[MockAuth {
        address: user,
        invoke: &invoke,
    }]);
}

pub fn assert_event<E: Event>(env: &Env, id: &Address, ev: E) {
    let want = ev.to_xdr(env, id);
    assert!(
        events(env, id).contains(&want),
        "{} event not found among emitted events",
        core::any::type_name::<E>()
    );
}

pub fn assert_latest_event<E: Event>(env: &Env, id: &Address, ev: E) {
    let want = ev.to_xdr(env, id);
    assert_eq!(
        events(env, id).last(),
        Some(&want),
        "{} was not the most recent event emitted",
        core::any::type_name::<E>()
    );
}

pub fn events(env: &Env, id: &Address) -> alloc::vec::Vec<ContractEvent> {
    env.events().all().filter_by_contract(id).events().to_vec()
}

pub trait Harness {
    fn env(&self) -> &Env;
    fn id(&self) -> &Address;

    fn events(&self) -> alloc::vec::Vec<ContractEvent> {
        events(self.env(), self.id())
    }

    fn assert_event<E: Event>(&self, ev: E) {
        assert_event(self.env(), self.id(), ev)
    }

    fn assert_latest_event<E: Event>(&self, ev: E) {
        assert_latest_event(self.env(), self.id(), ev)
    }

    fn instance_ttl(&self) -> u32 {
        self.env()
            .as_contract(self.id(), || self.env().storage().instance().get_ttl())
    }
}

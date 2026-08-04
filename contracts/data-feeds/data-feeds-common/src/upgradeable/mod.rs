use soroban_sdk::{contractevent, contracttrait, BytesN, Env};

pub type WasmHash = BytesN<32>;

#[contractevent(topics = ["Upgraded"])]
#[derive(Clone, Debug)]
pub struct Upgraded {
    pub new_wasm_hash: BytesN<32>,
}

#[contracttrait]
pub trait Upgradeable {
    fn upgrade(env: &Env, new_wasm_hash: BytesN<32>) {
        stellar_access::ownable::enforce_owner_auth(env);
        env.deployer()
            .update_current_contract_wasm(new_wasm_hash.clone());
        Upgraded { new_wasm_hash }.publish(env);
    }
}

#[cfg(test)]
mod tests {
    use crate::test_utils::{assert_event, authorize, deploy, MockClient};
    use crate::Upgraded;
    use soroban_sdk::{symbol_short, testutils::Address as _, Address, Env, Val, Vec};

    fn peek(env: &Env, id: &Address) -> u32 {
        env.invoke_contract(id, &symbol_short!("peek"), Vec::<Val>::new(env))
    }

    mod upgrade {
        use super::*;

        const TARGET_WASM: &[u8] = include_bytes!(concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/test_fixtures/upgrade_target.wasm"
        ));

        #[test]
        #[should_panic(expected = "Error(Auth, InvalidAction)")]
        fn by_non_owner_panics() {
            let (env, id) = deploy();
            let attacker = Address::generate(&env);
            let hash = env.deployer().upload_contract_wasm(TARGET_WASM);
            authorize(&env, &id, &attacker, "upgrade", (hash.clone(),));
            MockClient::new(&env, &id).upgrade(&hash);
        }

        #[test]
        fn emits_upgraded_event() {
            let (env, id) = deploy();
            env.mock_all_auths();
            let hash = env.deployer().upload_contract_wasm(TARGET_WASM);
            MockClient::new(&env, &id).upgrade(&hash);
            assert_event(
                &env,
                &id,
                Upgraded {
                    new_wasm_hash: hash,
                },
            );
        }

        #[test]
        fn swaps_in_the_uploaded_wasm() {
            let (env, id) = deploy();
            env.mock_all_auths();
            let hash = env.deployer().upload_contract_wasm(TARGET_WASM);
            MockClient::new(&env, &id).upgrade(&hash);
            assert_eq!(
                peek(&env, &id),
                0,
                "peek exists only in the fixture wasm, so a successful call proves the swap"
            );
        }

        #[test]
        fn preserves_instance_storage() {
            let (env, id) = deploy();
            env.mock_all_auths();
            let c = MockClient::new(&env, &id);
            c.poke(&42);
            let hash = env.deployer().upload_contract_wasm(TARGET_WASM);
            c.upgrade(&hash);
            assert_eq!(
                peek(&env, &id),
                42,
                "the fixture's peek reads the slot the old code wrote"
            );
        }
    }
}

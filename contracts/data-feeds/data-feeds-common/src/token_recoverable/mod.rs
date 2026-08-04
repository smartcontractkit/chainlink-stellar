use soroban_sdk::{contractevent, contracttrait, Address, Env};

#[contractevent(topics = ["TokenRecovered"])]
#[derive(Clone, Debug)]
pub struct TokenRecovered {
    pub token: Address,
    pub to: Address,
    pub amount: i128,
}

#[contracttrait]
pub trait TokenRecoverable {
    fn recover_tokens(env: &Env, token: Address, to: Address, amount: i128) {
        stellar_access::ownable::enforce_owner_auth(env);
        soroban_sdk::token::TokenClient::new(env, &token).transfer(
            &env.current_contract_address(),
            &to,
            &amount,
        );
        TokenRecovered { token, to, amount }.publish(env);
    }
}

#[cfg(test)]
mod tests {
    use crate::test_utils::{assert_event, authorize, deploy, MockClient};
    use crate::TokenRecovered;
    use soroban_sdk::{
        testutils::Address as _,
        token::{StellarAssetClient, TokenClient},
        Address, Env,
    };

    fn sac_token(env: &Env) -> Address {
        let admin = Address::generate(env);
        env.register_stellar_asset_contract_v2(admin).address()
    }

    mod recover_tokens {
        use super::*;

        #[test]
        fn transfers_amount_to_destination() {
            let (env, id) = deploy();
            env.mock_all_auths();
            let token = sac_token(&env);
            let dest = Address::generate(&env);

            StellarAssetClient::new(&env, &token).mint(&id, &1_000);
            MockClient::new(&env, &id).recover_tokens(&token, &dest, &600);

            let t = TokenClient::new(&env, &token);
            assert_eq!(t.balance(&dest), 600);
            assert_eq!(
                t.balance(&id),
                1_000 - 600,
                "remainder stays with the contract"
            );
        }

        #[test]
        fn emits_recovered_event() {
            let (env, id) = deploy();
            env.mock_all_auths();
            let token = sac_token(&env);
            let dest = Address::generate(&env);
            StellarAssetClient::new(&env, &token).mint(&id, &1_000);
            MockClient::new(&env, &id).recover_tokens(&token, &dest, &600);
            assert_event(
                &env,
                &id,
                TokenRecovered {
                    token,
                    to: dest,
                    amount: 600,
                },
            );
        }

        #[test]
        #[should_panic(expected = "Error(Auth, InvalidAction)")]
        fn from_non_owner_panics() {
            let (env, id) = deploy();
            let token = sac_token(&env);
            let attacker = Address::generate(&env);
            let dest = Address::generate(&env);
            env.mock_all_auths();
            StellarAssetClient::new(&env, &token).mint(&id, &1_000);
            authorize(
                &env,
                &id,
                &attacker,
                "recover_tokens",
                (token.clone(), dest.clone(), 600_i128),
            );
            MockClient::new(&env, &id).recover_tokens(&token, &dest, &600);
        }
    }
}

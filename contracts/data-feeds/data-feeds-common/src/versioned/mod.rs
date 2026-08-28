use soroban_sdk::{contracttrait, Env, String};

#[contracttrait]
pub trait Versioned {
    fn version(env: Env) -> u32;
    fn type_and_version(env: Env) -> String;
}

#[cfg(test)]
mod tests {
    use crate::test_utils::{deploy, MockClient};
    use soroban_sdk::String;

    mod version {
        use super::*;

        #[test]
        fn dispatches_to_the_implementing_contract() {
            let (env, id) = deploy();
            assert_eq!(MockClient::new(&env, &id).version(), 7);
        }
    }

    mod type_and_version {
        use super::*;

        #[test]
        fn dispatches_to_the_implementing_contract() {
            let (env, id) = deploy();
            assert_eq!(
                MockClient::new(&env, &id).type_and_version(),
                String::from_str(&env, "Mock 7.0.0")
            );
        }
    }
}

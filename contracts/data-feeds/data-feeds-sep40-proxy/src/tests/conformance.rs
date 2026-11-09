//! Pins the wire format. The SEP-40 types are declared locally rather than imported from the
//! Script3 crate, so these tests are what keep them interchangeable with it: any SEP-40 consumer
//! calls the contract by function name with XDR-encoded arguments, never through our Rust types.

use super::harness::*;

use soroban_sdk::{
    testutils::Address as _, vec, IntoVal, Map, Symbol, TryFromVal, Val, Vec as SorobanVec,
};

fn call<T: TryFromVal<Env, Val>>(a: &Adapter, name: &str, args: SorobanVec<Val>) -> T {
    a.env
        .invoke_contract(&a.id, &Symbol::new(&a.env, name), args)
}

mod asset_encoding {
    use super::*;

    #[test]
    fn other_is_a_two_element_vec_of_symbol_and_symbol() {
        let env = Env::default();
        let val: Val = Asset::Other(Symbol::new(&env, "BTC")).into_val(&env);
        let parts = SorobanVec::<Val>::try_from_val(&env, &val).unwrap();

        assert_eq!(parts.len(), 2);
        assert_eq!(
            Symbol::try_from_val(&env, &parts.get_unchecked(0)).unwrap(),
            Symbol::new(&env, "Other")
        );
        assert_eq!(
            Symbol::try_from_val(&env, &parts.get_unchecked(1)).unwrap(),
            Symbol::new(&env, "BTC")
        );
    }

    #[test]
    fn stellar_is_a_two_element_vec_of_symbol_and_address() {
        let env = Env::default();
        let token = Address::generate(&env);
        let val: Val = Asset::Stellar(token.clone()).into_val(&env);
        let parts = SorobanVec::<Val>::try_from_val(&env, &val).unwrap();

        assert_eq!(parts.len(), 2);
        assert_eq!(
            Symbol::try_from_val(&env, &parts.get_unchecked(0)).unwrap(),
            Symbol::new(&env, "Stellar")
        );
        assert_eq!(
            Address::try_from_val(&env, &parts.get_unchecked(1)).unwrap(),
            token
        );
    }

    #[test]
    fn round_trips_from_a_hand_built_vec() {
        let env = Env::default();
        let hand_built: SorobanVec<Val> = vec![
            &env,
            Symbol::new(&env, "Other").into_val(&env),
            Symbol::new(&env, "ETH").into_val(&env),
        ];
        let asset = Asset::try_from_val(&env, &hand_built.to_val()).unwrap();

        assert_eq!(asset, Asset::Other(Symbol::new(&env, "ETH")));
    }
}

mod price_data_encoding {
    use super::*;

    #[test]
    fn is_a_map_keyed_by_price_and_timestamp() {
        let env = Env::default();
        let val: Val = PriceData {
            price: 123,
            timestamp: 456,
        }
        .into_val(&env);
        let map = Map::<Symbol, Val>::try_from_val(&env, &val).unwrap();

        assert_eq!(map.len(), 2);
        assert_eq!(
            i128::try_from_val(&env, &map.get(Symbol::new(&env, "price")).unwrap()).unwrap(),
            123
        );
        assert_eq!(
            u64::try_from_val(&env, &map.get(Symbol::new(&env, "timestamp")).unwrap()).unwrap(),
            456
        );
    }
}

mod invoked_by_function_name {
    use super::*;

    #[test]
    fn base_on_the_wire_is_other_wrapping_the_constructor_symbol() {
        for name in ["USD", "GBP", "EUR"] {
            let a = Adapter::deploy_with_base(name);
            let raw: Val = call(&a, "base", vec![&a.env]);
            let parts = SorobanVec::<Val>::try_from_val(&a.env, &raw).unwrap();

            assert_eq!(parts.len(), 2, "{name}");
            assert_eq!(
                Symbol::try_from_val(&a.env, &parts.get_unchecked(0)).unwrap(),
                Symbol::new(&a.env, "Other"),
                "{name}"
            );
            assert_eq!(
                Symbol::try_from_val(&a.env, &parts.get_unchecked(1)).unwrap(),
                Symbol::new(&a.env, name),
                "{name}"
            );
        }
    }

    #[test]
    fn consumer_interface_is_callable_by_function_name() {
        let a = Adapter::deploy();
        let btc = a.map("BTC", 1, 100, 10);

        let base: Asset = call(&a, "base", vec![&a.env]);
        assert_eq!(base, a.asset("USD"), "base");

        let listed: SorobanVec<Asset> = call(&a, "assets", vec![&a.env]);
        assert_eq!(listed, vec![&a.env, btc.clone()], "assets");

        let decimals: u32 = call(&a, "decimals", vec![&a.env]);
        assert_eq!(decimals, 18, "decimals");

        let resolution: u32 = call(&a, "resolution", vec![&a.env]);
        assert_eq!(resolution, 1, "resolution");

        let args: SorobanVec<Val> = vec![&a.env, btc.clone().into_val(&a.env)];

        let data: Option<PriceData> = call(&a, "lastprice", args);
        assert_eq!(
            data,
            Some(PriceData {
                price: 100,
                timestamp: 10
            }),
            "lastprice"
        );

        let args: SorobanVec<Val> = vec![&a.env, a.asset("NOPE").into_val(&a.env)];

        let data: Option<PriceData> = call(&a, "lastprice", args);
        assert_eq!(data, None, "lastprice for unknown asset");

        let price_args: SorobanVec<Val> =
            vec![&a.env, btc.into_val(&a.env), 10u64.into_val(&a.env)];
        let price: Option<PriceData> = call(&a, "price", price_args);
        assert_eq!(price, None, "price");

        let prices_args: SorobanVec<Val> =
            vec![&a.env, btc.into_val(&a.env), 3u32.into_val(&a.env)];
        let prices: Option<SorobanVec<PriceData>> = call(&a, "prices", prices_args);
        assert_eq!(prices, None, "prices");
    }
}

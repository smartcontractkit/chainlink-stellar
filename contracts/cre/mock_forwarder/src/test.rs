extern crate std;

use soroban_sdk::testutils::{Address as _, AuthorizedFunction, Events as _};
use soroban_sdk::{
    contract, contracterror, contractimpl, symbol_short, vec, Address, Bytes, BytesN, Env, IntoVal,
    Symbol, Vec,
};

use crate::{
    Ed25519Signature, MockForwarder, MockForwarderClient, MockForwarderError, TransmissionState,
};

const METADATA_LENGTH: usize = 109;
const REPORT_CONTEXT_LENGTH: usize = 96;

#[contracterror]
#[derive(Copy, Clone, Eq, PartialEq)]
#[repr(u32)]
pub enum ReceiverError {
    Rejected = 1,
}

/// Records the sender, metadata and payload it receives.
#[contract]
pub struct RecordingReceiver;

#[contractimpl]
impl RecordingReceiver {
    pub fn on_report(env: Env, sender: Address, metadata: Bytes, payload: Bytes) {
        sender.require_auth();
        env.storage()
            .instance()
            .set(&symbol_short!("sender"), &sender);
        env.storage()
            .instance()
            .set(&symbol_short!("meta"), &metadata);
        env.storage()
            .instance()
            .set(&symbol_short!("payload"), &payload);
    }

    pub fn last(env: Env) -> (Address, Bytes, Bytes) {
        let s = env.storage().instance();
        (
            s.get(&symbol_short!("sender")).unwrap(),
            s.get(&symbol_short!("meta")).unwrap(),
            s.get(&symbol_short!("payload")).unwrap(),
        )
    }
}

/// Rejects until toggled, to exercise Failed → retry → Succeeded.
#[contract]
pub struct ToggleReceiver;

#[contractimpl]
impl ToggleReceiver {
    pub fn set_reject(env: Env, reject: bool) {
        env.storage().instance().set(&symbol_short!("REJ"), &reject);
    }

    pub fn on_report(
        env: Env,
        _sender: Address,
        _metadata: Bytes,
        _payload: Bytes,
    ) -> Result<(), ReceiverError> {
        if env
            .storage()
            .instance()
            .get(&symbol_short!("REJ"))
            .unwrap_or(true)
        {
            Err(ReceiverError::Rejected)
        } else {
            Ok(())
        }
    }
}

/// A Wasm contract without `on_report`.
#[contract]
pub struct WrongSymbolReceiver;

#[contractimpl]
impl WrongSymbolReceiver {
    pub fn other(_env: Env) -> u32 {
        1
    }
}

struct Fixture {
    env: Env,
    forwarder: Address,
    transmitter: Address,
}

impl Fixture {
    fn new() -> Self {
        let env = Env::default();
        env.mock_all_auths();
        let forwarder = env.register(MockForwarder, ());
        let transmitter = Address::generate(&env);
        Fixture {
            env,
            forwarder,
            transmitter,
        }
    }

    fn client(&self) -> MockForwarderClient<'_> {
        MockForwarderClient::new(&self.env, &self.forwarder)
    }

    fn no_sigs(&self) -> Vec<Ed25519Signature> {
        Vec::new(&self.env)
    }
}

/// 109-byte metadata header (version 1, execution id = [exec; 32], report id
/// 0x0001, metadata tail filled with a recognizable pattern) + payload.
fn raw_report(env: &Env, exec: u8, payload: &[u8]) -> Bytes {
    let mut raw = [0u8; METADATA_LENGTH];
    raw[0] = 1;
    raw[1..33].fill(exec);
    for (i, b) in raw[45..107].iter_mut().enumerate() {
        *b = i as u8;
    }
    raw[107] = 0x00;
    raw[108] = 0x01;
    let mut out = Bytes::from_slice(env, &raw);
    out.extend_from_slice(payload);
    out
}

fn ctx(env: &Env) -> Bytes {
    Bytes::from_slice(env, &[0u8; REPORT_CONTEXT_LENGTH])
}

fn exec_id(env: &Env, exec: u8) -> BytesN<32> {
    BytesN::from_array(env, &[exec; 32])
}

fn report_id(env: &Env) -> BytesN<2> {
    BytesN::from_array(env, &[0, 1])
}

#[test]
fn delivers_without_signatures_and_sender_is_forwarder() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());
    let raw = raw_report(&fx.env, 7, b"hello");

    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );

    // The transmitter authorized the report call.
    let auths = fx.env.auths();
    assert_eq!(auths[0].0, fx.transmitter);
    assert_eq!(
        auths[0].1.function,
        AuthorizedFunction::Contract((
            fx.forwarder.clone(),
            Symbol::new(&fx.env, "report"),
            (
                fx.transmitter.clone(),
                receiver.clone(),
                raw.clone(),
                ctx(&fx.env),
                fx.no_sigs()
            )
                .into_val(&fx.env),
        ))
    );

    let (sender, metadata, payload) = RecordingReceiverClient::new(&fx.env, &receiver).last();
    assert_eq!(sender, fx.forwarder);
    assert_eq!(metadata, raw.slice(45..109));
    assert_eq!(payload, Bytes::from_slice(&fx.env, b"hello"));

    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 7), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::Succeeded);
    assert_eq!(info.transmitter, Some(fx.transmitter.clone()));
}

#[test]
fn ignores_arbitrary_signatures() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());
    let junk = vec![
        &fx.env,
        Ed25519Signature {
            public_key: BytesN::from_array(&fx.env, &[9u8; 32]),
            signature: BytesN::from_array(&fx.env, &[9u8; 64]),
        },
    ];
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw_report(&fx.env, 1, b"x"),
        &ctx(&fx.env),
        &junk,
    );
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 1), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::Succeeded);
}

#[test]
fn emits_report_processed() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw_report(&fx.env, 2, b"x"),
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let events = fx.env.events().all();
    let (contract, topics, data) = events
        .events()
        .last()
        .cloned()
        .map(|e| {
            let soroban_sdk::xdr::ContractEventBody::V0(body) = e.body;
            (e.contract_id, body.topics, body.data)
        })
        .unwrap();
    assert!(contract.is_some());
    assert_eq!(topics.len(), 4);
    assert_eq!(
        topics[0],
        soroban_sdk::xdr::ScVal::Symbol("forwarder_ReportProcessed".try_into().unwrap())
    );
    assert_eq!(data, soroban_sdk::xdr::ScVal::Bool(true));
}

#[test]
fn replay_of_succeeded_report_is_rejected() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());
    let raw = raw_report(&fx.env, 3, b"x");
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let err = fx.client().try_report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    assert_eq!(err, Err(Ok(MockForwarderError::AlreadyProcessed)));
}

#[test]
fn failed_receiver_is_retryable() {
    let fx = Fixture::new();
    let receiver = fx.env.register(ToggleReceiver, ());
    let raw = raw_report(&fx.env, 4, b"x");

    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 4), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::Failed);

    ToggleReceiverClient::new(&fx.env, &receiver).set_reject(&false);
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 4), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::Succeeded);
}

#[test]
fn receiver_without_on_report_is_failed() {
    // Matches the production forwarder: Soroban reports a missing function as
    // a contract error, so the delivery is Failed (retryable), not terminal.
    let fx = Fixture::new();
    let receiver = fx.env.register(WrongSymbolReceiver, ());
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw_report(&fx.env, 5, b"x"),
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 5), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::Failed);
}

#[test]
fn non_wasm_receiver_is_terminal() {
    let fx = Fixture::new();
    // A generated address has no contract executable.
    let receiver = Address::generate(&fx.env);
    let raw = raw_report(&fx.env, 6, b"x");
    fx.client().report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 6), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::InvalidReceiver);

    let err = fx.client().try_report(
        &fx.transmitter,
        &receiver,
        &raw,
        &ctx(&fx.env),
        &fx.no_sigs(),
    );
    assert_eq!(err, Err(Ok(MockForwarderError::AlreadyProcessed)));
}

#[test]
fn structural_checks_match_production() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());

    let short = Bytes::from_slice(&fx.env, &[1u8; 10]);
    assert_eq!(
        fx.client().try_report(
            &fx.transmitter,
            &receiver,
            &short,
            &ctx(&fx.env),
            &fx.no_sigs()
        ),
        Err(Ok(MockForwarderError::InvalidReport))
    );

    let bad_ctx = Bytes::from_slice(&fx.env, &[0u8; 3]);
    assert_eq!(
        fx.client().try_report(
            &fx.transmitter,
            &receiver,
            &raw_report(&fx.env, 8, b"x"),
            &bad_ctx,
            &fx.no_sigs()
        ),
        Err(Ok(MockForwarderError::InvalidReportContext))
    );

    let mut bad_version = raw_report(&fx.env, 8, b"x");
    bad_version.set(0, 2);
    assert_eq!(
        fx.client().try_report(
            &fx.transmitter,
            &receiver,
            &bad_version,
            &ctx(&fx.env),
            &fx.no_sigs()
        ),
        Err(Ok(MockForwarderError::InvalidReportVersion))
    );
}

#[test]
fn type_and_version() {
    let fx = Fixture::new();
    assert_eq!(
        fx.client().type_and_version(),
        soroban_sdk::String::from_str(&fx.env, "MockForwarder 1.0.0")
    );
}

#[test]
fn unknown_transmission_is_not_attempted() {
    let fx = Fixture::new();
    let receiver = fx.env.register(RecordingReceiver, ());
    let info =
        fx.client()
            .get_transmission_info(&receiver, &exec_id(&fx.env, 9), &report_id(&fx.env));
    assert_eq!(info.state, TransmissionState::NotAttempted);
    assert_eq!(info.transmitter, None);
}

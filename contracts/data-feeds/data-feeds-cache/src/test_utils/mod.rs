use soroban_sdk::{
    testutils::Address as _, vec, xdr::ToXdr, Address, Bytes, BytesN, Env, String, Vec, I256,
};

use crate::interface::types::{
    DataId, FeedConfig, ReportEntry, WireDataId, WorkflowName, WorkflowOwner, WorkflowPermission,
};
use crate::interface::FeedConfigEntry;
use crate::storage::CanonicalId;
use crate::DataFeedsCacheClient;

pub const DEFAULT_BYTE7: u8 = 0x32;
pub const DEFAULT_DESC: &str = "BTC/USD";

pub fn mock_data_id(env: &Env) -> DataId {
    BytesN::from_array(env, &[0x32; 16])
}

pub fn round_ttl(env: &Env, id: &DataId, round_id: u64) -> u32 {
    use soroban_sdk::testutils::storage::Temporary as _;

    env.storage()
        .temporary()
        .get_ttl(&crate::storage::DataKey::Round(
            CanonicalId::new(env, id).as_bytes().clone(),
            round_id,
        ))
}

pub fn mock_feed_id(env: &Env, tag: u8) -> DataId {
    mock_feed_id_with(env, DEFAULT_BYTE7, tag)
}

pub fn zero_feed_id(env: &Env) -> DataId {
    mock_feed_id_with(env, 0, 0)
}

pub fn mock_feed_id_with(env: &Env, byte7: u8, tag: u8) -> DataId {
    let mut a = [0u8; 16];
    a[7] = byte7;
    a[15] = tag;
    BytesN::from_array(env, &a)
}

pub fn mock_wire_id(env: &Env, tag_hi: u8, tag_lo: u8) -> WireDataId {
    mock_wire_id_with(env, DEFAULT_BYTE7, tag_hi, tag_lo)
}

pub fn mock_wire_id_with(env: &Env, byte7: u8, tag_hi: u8, tag_lo: u8) -> WireDataId {
    let mut a = [0u8; 32];
    a[7] = byte7;
    a[15] = tag_hi;
    a[31] = tag_lo;
    BytesN::from_array(env, &a)
}

pub fn mock_wf_owner(env: &Env) -> WorkflowOwner {
    BytesN::from_array(env, &[0x11; 20])
}

pub fn mock_wf_name(env: &Env) -> WorkflowName {
    BytesN::from_array(env, &[0x22; 10])
}

pub fn metadata(env: &Env, owner: &WorkflowOwner, name: &WorkflowName) -> Bytes {
    let mut buf = [0u8; 64];
    buf[32..42].copy_from_slice(&name.to_array());
    buf[42..62].copy_from_slice(&owner.to_array());
    Bytes::from_array(env, &buf)
}

pub fn mock_metadata(env: &Env) -> Bytes {
    metadata(env, &mock_wf_owner(env), &mock_wf_name(env))
}

pub fn report(env: &Env, entries: &[(WireDataId, i128, u64)]) -> Bytes {
    let mut v: Vec<ReportEntry> = Vec::new(env);
    for (d, a, t) in entries {
        v.push_back(ReportEntry {
            data_id: d.clone(),
            answer: I256::from_i128(env, *a),
            timestamp: *t,
        });
    }
    v.to_xdr(env)
}

pub fn mock_permission(env: &Env, sender: &Address) -> WorkflowPermission {
    WorkflowPermission {
        allowed_sender: sender.clone(),
        allowed_workflow_owner: mock_wf_owner(env),
        allowed_workflow_name: mock_wf_name(env),
    }
}

pub fn mock_feed_config(env: &Env, id: &DataId, sender: &Address, desc: &str) -> FeedConfigEntry {
    feed_config(env, id, mock_permission(env, sender), desc)
}

pub fn feed_config(
    env: &Env,
    id: &DataId,
    perm: WorkflowPermission,
    desc: &str,
) -> FeedConfigEntry {
    FeedConfigEntry {
        data_id: id.clone(),
        config: FeedConfig {
            description: String::from_str(env, desc),
            workflow_permissions: vec![env, perm],
        },
    }
}

pub fn permit_feed(env: &Env, cache: &Address, data_id: &DataId) -> Address {
    let c = DataFeedsCacheClient::new(env, cache);
    let admin = Address::generate(env);
    let sender = Address::generate(env);
    c.add_feed_admin(&admin);
    c.set_feed_configs(
        &admin,
        &vec![env, mock_feed_config(env, data_id, &sender, DEFAULT_DESC)],
    );
    sender
}

pub fn seed_round(
    env: &Env,
    cache: &Address,
    data_id: &DataId,
    sender: &Address,
    answer: i128,
    ts: u64,
) {
    let a = data_id.to_array();
    let wid = mock_wire_id_with(env, a[7], a[15], 0);
    DataFeedsCacheClient::new(env, cache).on_report(
        sender,
        &mock_metadata(env),
        &report(env, &[(wid, answer, ts)]),
    );
}

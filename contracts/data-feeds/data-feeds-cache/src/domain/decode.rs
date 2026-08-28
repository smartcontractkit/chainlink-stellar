use soroban_sdk::xdr::FromXdr;
use soroban_sdk::{Bytes, Env, Vec};

use crate::interface::types::{
    Metadata, ReportEntry, ReportId, WorkflowCid, WorkflowName, WorkflowOwner,
};
use crate::interface::CacheError;

pub(crate) fn decode_report(env: &Env, report: &Bytes) -> Result<Vec<ReportEntry>, CacheError> {
    Vec::<ReportEntry>::from_xdr(env, report).map_err(|_| CacheError::MalformedReport)
}

pub(crate) fn decode_metadata(_env: &Env, metadata: &Bytes) -> Result<Metadata, CacheError> {
    if metadata.len() != 64 {
        return Err(CacheError::MalformedReport);
    }
    let workflow_cid =
        WorkflowCid::try_from(metadata.slice(0..32)).map_err(|_| CacheError::MalformedReport)?;
    let workflow_name =
        WorkflowName::try_from(metadata.slice(32..42)).map_err(|_| CacheError::MalformedReport)?;
    let workflow_owner =
        WorkflowOwner::try_from(metadata.slice(42..62)).map_err(|_| CacheError::MalformedReport)?;
    let report_id =
        ReportId::try_from(metadata.slice(62..64)).map_err(|_| CacheError::MalformedReport)?;
    Ok(Metadata {
        workflow_cid,
        workflow_name,
        workflow_owner,
        report_id,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_sdk::xdr::ToXdr;
    use soroban_sdk::{BytesN, I256};

    const CID: [u8; 32] = [
        0x15, 0xc6, 0x31, 0xd2, 0x95, 0xef, 0x5e, 0x32, 0xde, 0xb9, 0x9a, 0x10, 0xee, 0x68, 0x04,
        0xbc, 0x4a, 0xf1, 0x38, 0x55, 0x68, 0xf9, 0xb3, 0x36, 0x3f, 0x65, 0x52, 0xac, 0x6d, 0xbb,
        0x2c, 0xef,
    ];
    const NAME: [u8; 10] = [0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xaa, 0xbb, 0xcc, 0xdd, 0xee];
    const OWNER: [u8; 20] = [0x55; 20];
    const RID: [u8; 2] = [0x99, 0x88];

    const REPORT_DATA_ID: [u8; 32] = [0xAB; 32];
    const REPORT_ANSWER: i128 = 123_456;
    const REPORT_TIMESTAMP: u64 = 100;

    fn dummy_metadata() -> [u8; 64] {
        let mut md = [0u8; 64];
        md[0..32].copy_from_slice(&CID);
        md[32..42].copy_from_slice(&NAME);
        md[42..62].copy_from_slice(&OWNER);
        md[62..64].copy_from_slice(&RID);
        md
    }

    fn dummy_report(env: &Env) -> Bytes {
        let entry = ReportEntry {
            data_id: BytesN::from_array(env, &REPORT_DATA_ID),
            answer: I256::from_i128(env, REPORT_ANSWER),
            timestamp: REPORT_TIMESTAMP,
        };
        Vec::from_array(env, [entry]).to_xdr(env)
    }

    #[test]
    fn decode_report_recovers_dummy_report() {
        let env = Env::default();
        let decoded = decode_report(&env, &dummy_report(&env)).unwrap();
        assert_eq!(decoded.len(), 1);
        let r = decoded.get(0).unwrap();
        assert_eq!(r.data_id, BytesN::from_array(&env, &REPORT_DATA_ID));
        assert_eq!(r.answer, I256::from_i128(&env, REPORT_ANSWER));
        assert_eq!(r.timestamp, REPORT_TIMESTAMP);
    }

    #[test]
    fn decode_report_returns_error_for_malformed_report() {
        let env = Env::default();
        let scalar = 42u32.to_xdr(&env);
        assert!(matches!(
            decode_report(&env, &scalar),
            Err(CacheError::MalformedReport)
        ));
    }

    #[test]
    fn decode_metadata_recovers_dummy_metadata() {
        let env = Env::default();
        let bytes = Bytes::from_array(&env, &dummy_metadata());
        let m = decode_metadata(&env, &bytes).unwrap();
        assert_eq!(m.workflow_cid, BytesN::from_array(&env, &CID));
        assert_eq!(m.workflow_name, BytesN::from_array(&env, &NAME));
        assert_eq!(m.workflow_owner, BytesN::from_array(&env, &OWNER));
        assert_eq!(m.report_id, BytesN::from_array(&env, &RID));
    }

    #[test]
    fn decode_metadata_returns_error_for_incorrect_metadata_length() {
        let env = Env::default();
        for len in [0usize, 63, 65, 109] {
            let bytes = Bytes::from_array(&env, &[0u8; 128]).slice(0..len as u32);
            assert!(
                matches!(
                    decode_metadata(&env, &bytes),
                    Err(CacheError::MalformedReport)
                ),
                "len {len}"
            );
        }
    }
}

use soroban_sdk::contracterror;

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
#[repr(u32)]
pub enum Sep40ProxyError {
    AssetAlreadyMapped = 200,
    AssetNotMapped = 201,
    DataIdInUse = 202,
    InvalidDataId = 203,
    FeedUnreadable = 204,
    AssetIsBase = 205,
}

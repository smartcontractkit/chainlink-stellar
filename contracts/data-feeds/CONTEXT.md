# Data Feeds (Stellar)

On-chain cache and proxy for oracle price feeds on Stellar. The cache stores
feed configurations and round history; the proxy serves reads to consumers.

## Language

**Feed**:
A single oracle data stream, serving timestamped price rounds at one stored decimal precision.

**Data ID**:
A 16-byte identifier presented by a caller to address a **Feed**; byte 7 (the **decimals byte**) encodes the precision the caller wants, as `0x20 + decimals`.

**Canonical ID**:
A **Data ID** with the **decimals byte** zeroed — the storage identity of a **Feed**. Never presented by callers; never addressable as a request.

**Stored decimals**:
The single decimal precision a **Feed** is configured with; all stored answers are at this scale.

**Requested decimals**:
The precision a caller asks for, carried in the **decimals byte** of the **Data ID** they present. Must be ≤ **stored decimals**.

**Downscaling**:
Converting an answer from **stored decimals** to a lower **requested decimals**. Performed by the proxy, never by the cache.

**Round**:
One timestamped answer in a **Feed**'s history.

## Relationships

- A **Feed** has exactly one **Canonical ID** and one **stored decimals**; many **Data IDs** (one per **requested decimals**) address the same **Feed**.
- A **Feed** accumulates many **Rounds**, all stored at **stored decimals**.
- The proxy **downscales** a **Round** from **stored decimals** to **requested decimals**; the cache stores and returns raw answers only.

## Example dialogue

> **Dev:** "A customer wants BTC/USD at 8 decimals, but the feed is configured at 18. Do we store a second copy?"
> **Domain expert:** "No — both **Data IDs** map to the same **Canonical ID**, so there's one config and one history at 18 **stored decimals**. The proxy **downscales** to the **requested decimals** on read."

## Flagged ambiguities

- "data ID" was used for both the storage key and the per-request identifier — resolved: the storage key is always the **Canonical ID**; a **Data ID** with a non-zero **decimals byte** is a request, never a storage key.
- Conversion responsibility was ambiguous between cache and proxy — resolved: the cache never converts; **downscaling** lives only in the proxy.

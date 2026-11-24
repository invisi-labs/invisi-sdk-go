# Reuse signed Core tariff within its valid lifetime

User requests initialized fee-lock hot path8→7, expiry/binding changes refetch, preserve safety/recovery and paired diagnostic. HEAD remains already-rebased a51f78d; no commit/push/rebase/release/broadcast. Root also separately diagnoses/fixes TS CI fixture in isolated TS worktree, without changing Go scope.

Actual server fee_lock.go documents reusable time-bounded Core tariff. Signature binds chain/protocol/signer/token/feeRecipient/model and frozen rates/gas/deadline. Payer nonce, business amount and calldata are not tariff identity; server still recomputes actual intent gas and quote deadline stays within tariff and intent bounds. Source price changes within signed commitment do not require a new tariff query.

- Cache only on exact active initialized preparation capability + original client/config/discovery source/native epoch/HTTP policy guard; one bounded slot, copied immutable ticket/trust per operation. No JSON/global/durable ticket cache.
- Reverify signature, identity and expiry on each hit/use. Token or bound identity/receipt/model changes fetch new. Cache miss and expiry keep cold lockFee request, so first flow8 and valid repeat7. Ordinary no-fee-lock stays7; uninitialized paths retain live behavior.
- Initializer admission/retirement clears slot. Generation/CAS avoids stale fetch or failure replacing/evicting newer slot/authority. No IO/guard calls under cache locks. Scoped operation never swaps its ticket midway through pricing.
- Exact fee-lock expiry/invalid/gas-refusal or local invalidity retires matching slot; current pricing fails atomically, retry obtains fresh. Never downgrade to unlocked pricing or mutate authorized/frozen transactions. Packet recovery unchanged.

1. Freeze before current production. Same driver already supports advertised lock, actual signature verification, 3fixtures ×2nonce7/14 and originaljournal integrity.
2. Scoped owner implementation/TDD in rpc/broadcaster + independent review; root owns diagnostic/scripts/README/report.
3. Full9module boundary/race/vet/build +8 extracted archives, then sequential20ms paired legacy and fee-lock tests. Include startup costs and honest cold vs hit counts; no hidden prefetch cost.

# Go round 7 fee-lock reuse independent review

Result: **GREEN**. No actionable findings in the scoped round 7 delta.

Reviewed only the exact `/tmp/invisi-go-prepare-round7-20261125/before` baseline against the frozen current versions of the five files below. Preexisting round 2–6 changes were excluded from findings. No production or test files were edited, and no tests, timing runs, commits, rebases or pushes were performed during this review.

## Safety and concurrency

- The authority retains one immutable ticket value under an exact receipt and token. Uninitialized preparations do not retain tickets. New initialization and receipt retirement clear the slot and advance the fetch generation.
- Cache hits authenticate signature, deployment, recipient, token, model and expiry again. Scoped clients receive independent ticket and trust values, preserving the selected tariff across estimate and quote calls. Every scoped use verifies again.
- Network calls and caller validity guards run outside the authority mutex. Publication requires both the active receipt and current fetch generation. Older successful fetches cannot overwrite a newer slot, and older failures do not evict it.
- Local invalidity and the three exact fee-lock rejection code/message pairs evict only the matching entry pointer. An older scope cannot evict a replacement. Ambiguous RPC errors preserve the slot. Rejections propagate to the caller without an automatic pricing retry or tariff substitution.
- Receipt checks remain in place at admission and scoped use. Reinitialization and revocation do not revive old scoped authority. Recovery, signed packet persistence and submission behavior have no changes in this delta.

## API and server compatibility

The server implementation in `/Users/wx/Workspace/invisi-lab/invisi-broadcaster/internal/service/fee_lock.go` signs a reusable tariff without an intent hash or nonce. It recomputes gas for the actual request and enforces the signed and current gas ceilings. The quote path in `internal/service/service.go` bounds the quote deadline by the ticket expiry and request deadline. Reusing a valid tariff for another same-token preparation is compatible with that contract.

The hot path removes one `broadcaster_lockFee` call only after a prior successful initialized preparation; cold admission and renewal still require that request. No hidden prefetch was introduced. The prior round 6 low-level initialization callback tradeoff was treated as accepted scope.

## Verification evidence

Read the complete regression additions, including frozen ticket/trust isolation, expiry and signature revalidation, token/deployment changes, exact and ambiguous rejection handling, stale scope failures, newest-fetch publication, old fetch failure, reinitialization during IO, revocation and uninitialized freshness. Read the supplied `/tmp/invisi-go-prepare-round7-20261125/rpc-race.log`: all five RPC module packages passed `go test -race ./... -count=1`. This review did not independently rerun that suite or establish new performance timing evidence.

All frozen SHA256 fingerprints match the implementation report:

| File | SHA256 |
| --- | --- |
| `rpc/broadcaster/client.go` | `f268663f014e8dda05c6f6e6427a0bc236bc4576db9abe685ace6d21de8f2de1` |
| `rpc/broadcaster/fee_lock.go` | `1accbe0beb1a8b7883d2c119a006804a6ce9acfc666e0e18a60da2c7cdaee907` |
| `rpc/broadcaster/preparation_capability.go` | `6394d019af869e2d99a46ab3d718cc9ea7b75395660619baeae618bf4235becf` |
| `rpc/broadcaster/fee_lock_test.go` | `762d5930317966250f3a86e67bb5cba6b0e18466c083b65d9f13ee6c33cb5914` |
| `rpc/broadcaster/fee_lock_reuse_test.go` | `2765f94cc65e71cf4550647af54e5ea6babf2b61db025a4821bf5c42eb822d5f` |

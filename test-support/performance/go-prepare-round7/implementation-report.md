# Round 7 fee-lock reuse implementation

Scope: production and regression work only in rpc/broadcaster. No commits, pushes, rebases, TypeScript edits, module-wide suites, archives or timings.

## Behavior

- Only explicitly initialized exact active capability receipts retain a ticket. Ordinary uninitialized preparations remain fresh.
- One bounded in-memory slot per original client authority. Receipt identity pins chain/protocol/signer/configuration/capability; token must match too. Current price changes do not invalidate a signed frozen tariff.
- Cache hits reverify signed binding/model/deadline and run the receipt validity guard. Scoped clients receive independent ticket and trust copies and retain their selected ticket throughout pricing.
- Initialization and retirement clear the slot. A fetch sequence plus receipt comparison prevents older successful or failed network responses from replacing newer tickets or newer authority. HTTP IO and receipt guards run outside authority locks.
- Local invalidity/expiry and exact RPC rejection pairs (-32040/FEE_LOCK_EXPIRED, -32041/FEE_LOCK_INVALID, -32042/FEE_LOCK_GAS_EXCEEDED) evict only the matching slot. The current pricing call fails; later original preparation fetches fresh. Ambiguous errors retain the cache. Older scope errors cannot evict newer tickets.
- Existing quote verification, max-gas/deadline checks, submission policy/status behavior and operation persistence/recovery paths are unchanged.

## Evidence

RED: changed initialized two-flow expected lockFee count from 2 to 1; `go test ./broadcaster -run TestPreparationClientFreezesTicketWithoutMutatingSharedClient -count=1` failed with `status=0 tariff=2` before production edits.

Regression coverage includes valid reuse; estimate/getQuote carrying identical frozen signed tickets; immutable scoped ticket/trust copies; correctly signed expired cached/scoped tickets; cache and scoped tampering; token changes; deployment/signature binding mismatch and explicit reinitialization; all exact rejection pairs; ambiguous error preservation; stale rejection/local expiry against newer ticket; newest-wins cold fetch publication; old failed fetch; reinitialization during held fetch; guard revocation and no revival; uninitialized fresh preparations.

Focused race command: `go test -race ./... -count=1` from rpc module. Final output is in `rpc-race.log` alongside this report.

## Files and frozen hashes

- `rpc/broadcaster/client.go` SHA256 `f268663f014e8dda05c6f6e6427a0bc236bc4576db9abe685ace6d21de8f2de1`
- `rpc/broadcaster/fee_lock.go` SHA256 `1accbe0beb1a8b7883d2c119a006804a6ce9acfc666e0e18a60da2c7cdaee907`
- `rpc/broadcaster/preparation_capability.go` SHA256 `6394d019af869e2d99a46ab3d718cc9ea7b75395660619baeae618bf4235becf`
- `rpc/broadcaster/fee_lock_test.go` SHA256 `762d5930317966250f3a86e67bb5cba6b0e18466c083b65d9f13ee6c33cb5914`
- `rpc/broadcaster/fee_lock_reuse_test.go` SHA256 `2765f94cc65e71cf4550647af54e5ea6babf2b61db025a4821bf5c42eb822d5f`

## Final verification status

Independent scoped review: GREEN, no findings (`review-report.md`).

- `python3 scripts/test-modules.py`: exit 0. Boundary check plus race tests, vet and build passed across all nine modules, including examples. Evidence: `module-checks.log`.
- `python3 scripts/check-packages.py`: exit 0. All eight independently extracted module archives passed tests, vet and build. Evidence: `archive-checks.log`; final extracted evidence directory `/var/folders/kj/xt7gv9hn5f76zmw7vqdpl36m0000gn/T/invisi-go-package-check-zgqowshk`.
- All five frozen implementation file hashes still match the values above after both heavy checks. Exit codes and log/source hashes are recorded in `verification-status.json`.

SHA_HEAVY_DONE. No source edits or timing runs performed during final verification.

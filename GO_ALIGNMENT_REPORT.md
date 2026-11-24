# Go submit/read alignment

Scope: only `invisi-sdk-go/.worktree/submit-read-audit`. No commits, rebases, merges, releases or live sends. Existing chain-ID reuse patch in `capabilities.go` / `operation_export.go` remains intact. The before baseline is the frozen 701-file snapshot supplied by the root task, including prior audit work.

## Changes

- Atomic durable externalization writes the exact recovery Plan, calldata, proof, hash, uncertainty and externalization marker together. A rejected first update leaves the complete old record. A crash after that commit but before transport can resume the identical packet.
- The new owned `chain.HTTPBinding` has a fixed HTTP endpoint, private native client, epoch invalidation, disposal/idle-connection cleanup, and guarded read completions. Only that sealed binding gets successful immutable deployment receipts. Arbitrary providers keep full validation. Transaction lookup and authenticated native finality tags preserve reconciliation and finality checks.
- `Protocol.ReadAuthorizationNonce` reads current network identity and latest nonce. `InitializeBroadcasters` explicitly pays discovery/support once; receipts bind provider/epoch, token and actual HTTP-client policy. Failed/cancelled/empty refresh removes its marker; latest refresh owns publication; maps are bounded to 256; noncomparable policies remain uncached.
- Plain unscheduled no-permit Shield uses a paid-feature estimate, exact fee reprepare and signed quote. Only generic `-32000` quote refusal may refresh, bounded by attempts, actual price increase and user cap. Other shapes retain convergence loops.
- A native, direct, self-contained signature-funded Shield can reconstruct funding typed data and recover an EOA signature locally after a verified quote, then rely on exact broadcaster simulation/contract execution for execution validity. The private binding/epoch receipt survives local signing clones, never JSON persistence. Retired binding rejects signing; JSON-resumed uncached quotes take conservative live reads. Explicit verifiers and contract-wallet fallback retain live policy checks.
- Stock concrete broadcaster clients retain their own business validation when a caller wraps HTTP transport for auth/logging/write denial. Custom providers/verifiers, manual exports, private inputs, registry recipients, permits, scheduling and provenance keep conservative validation. First managed send keeps two network fences and current service requirements.
- Only exact guaranteed pre-send structured refusal reasons (`-32024/-32025/-32026`) on a genuinely first externalization can roll back. Rollback persists before returning; failure retains the complete frozen journal. Previously exported authority, unknown/resumed attempts and generic errors stay externalized. Known IDs use status; unknown IDs replay identical packets.

## Evidence

Meaningful red→green regressions cover rejected first frozen write, interruption immediately after atomic commit, lost response/restart/identical replay, status-only retry, custom externalization policy, typed refusal matrix and failed rollback persistence, prior manual export, provider lifetime races, current nonce, failed/concurrent/bounded discovery initialization, mutable HTTP-client policy, zero-RPC EOA authorization, explicit verifier policy, retired quote binding, JSON-resume fallback, actual HTTP confirmed reconciliation, and bad declared native-finality hash.

Final validation logs:

- `GO_ALIGNMENT_MODULE_CHECKS.log`: final post-alias all-module race/test/vet/build script passed all nine modules.
- `GO_ALIGNMENT_RPC_RACE.log`: RPC packages passed race tests.
- `GO_ALIGNMENT_SESSION_RACE.log`: session + internal transaction packages passed race tests.
- `GO_ALIGNMENT_PACKAGE_CHECKS.log`: isolated module archive test/vet/build script: final post-alias run passed all eight extracted public modules.
- Python CI reuse, release checks, release-note and authenticated metadata-script tests passed; one optional metadata synchronization test skipped.
- Independent final source review reported no remaining concrete finding. `git diff --check` passed.

## Performance boundaries and differences

The unified diagnostic is in `session/three_sdk_http_*_test.go` with its scripts under `test-support/performance/three-sdk-alignment`. Root coordination owns final frozen provenance and sequential 20 ms measurements; no elapsed-time claim is made here before those runs.

Go keeps four ordinary preparation reads: current network identity, latest chain-time/deadline head, token code, and the final canonical reservation fence. The existing empty-history `RefreshState` does not preload token facts without notes/grants, so it cannot fairly erase those reads. No artificial notes or new advisory anchor shortcut were introduced. The zero-delay actual diagnostic observed 25 HTTP reads before and 11 after per flow, across all six flows (after: nonce 2, preparation 4, lookup 0, quote 2, authorization 0, first submit 3). The finality-alias follow-up is request-neutral; final frozen provenance is managed by root coordination. Startup is counted separately once. JSON-resumed uncached prepared quotes intentionally lose the local signing receipt and read live state; legacy snapshot semantics remain unchanged.

Bad native finality headers/hashes retain the existing inconsistent classification through the documented shared error identity; null and unsupported responses keep their prior classifications. No unverified finality is promoted. The binding is a read/recovery connection; manual public-send backend APIs were not expanded.

# Independent Go capability review

Verdict: GREEN. No blocking correctness, concurrency, security or API compatibility finding in the scoped round6 delta.

## Scope and evidence

Reviewed the current candidate against the exact baseline `/tmp/invisi-go-prepare-round6-v2-20261125/before`, not Git HEAD. Scope: `rpc/broadcaster/{client,fee_lock,service_status,preparation_capability}.go`, `rpc/chain/broadcasters.go`, and their changed/new tests. Other earlier uncommitted work is not reviewed or attributed to round6. No production/test edits, commits, rebases, full tests or timed diagnostics were performed by this reviewer.

Reviewed owner evidence in `implementation-report.md`, `go-capability-focused.log` and `go-capability-rpc-race.log`. The owner reports focused broadcaster/chain tests and `go test -race ./rpc/... -count=1` passing. This review did not rerun tests or independently establish final full-suite or timed-call counts; root owns those checks.

## Findings

No actionable blocker found. Private copied capability values and exact trust/configuration checks avoid JSON/config authority restoration. Generation ownership and pointer-CAS retirement preserve newer initialization against old refresh/live-read completion. Retained and scoped pricing clients share revocation; old scoped fee-lock handles retain their original receipt and cannot revive after owner reinitialization. Discovery claims bind publication, exact RPC source, native provider epoch and captured transport/jar/timeout policy, including implicit default transport identity. The pending ordinary lookup regression preserves unpublished initialization. Locks are not held across broadcaster IO or receipt callbacks; no new lock cycle was found.

Missing capability is cacheable metadata only after actual validated initialization. Explicit null, malformed or unsupported advertised capability fails rather than selecting legacy pricing. Each advertised preparation still requests a fresh fee lock and validates its signature, model, token, recipient, chain/protocol and bounds. Live status/provenance/halt requirements remain mandatory for submission, and a capability change does not itself invalidate an already-signed otherwise-valid quote. Packet persistence, first-send fencing and recovery paths are unchanged by this scoped delta.

## Reviewed design tradeoff and documentation expectation

`InitializeTokenSupport` is an explicit public low-level metadata initialization API. Its caller supplies the validity callback and therefore owns its refresh/lifetime policy. `chain.Protocol.InitializeBroadcasters` supplies the native claim/source/epoch/policy guard. An always-successful caller callback cannot manufacture the validated service response, a signed fee lock/quote or transaction authorization, but it can deliberately retain metadata according to the caller's chosen lifetime. Plain `SupportsToken` and uninitialized `ForPreparation` remain fresh. Root confirmed that README now states this caller-owned lifetime and refresh-policy boundary explicitly, including that the low-level API conveys no native chain authority. This accepted API boundary is not a blocker.

Only fee-lock capability metadata is captured. Changed token support, scheduling or provenance does not independently retire the global fee-lock descriptor; live server/token checks and live submission requirements retain their separate roles. Failed/malformed/mismatched/halted live status or changed fee-lock descriptors retire the captured preparation receipt. This scope was confirmed by the root reviewer as the intended contract.

## Source binding

Whole source corpus: 724 files. Sorted relative paths matching `.go`, `.json`, `go.mod`, `go.sum`, `go.work`, `go.work.sum`; excluded `.git`, `.worktree`, `test-support` path components. `review-source-sha256.txt` contains `SHA256(file bytes) + two spaces + relative path + newline`. SHA256 of those exact manifest bytes: `0b1eaba865e2f04a45ee7f0789fd1e84fbe006fbd8196cdaf52ca20e4066dc0b`.

Scoped reviewed file hashes:

```text
e6d8eeb20241d0e49fe294aa10c3c58b6877f5b0b2e1996c59d4331d56fd2b43  rpc/broadcaster/client.go
8bd60d8a5b641884f960f3e25eb5ecf157f675e7973f00c313563802b60e138e  rpc/broadcaster/fee_lock.go
4cf39a939f648257c8ba9adae7b2857ce88bf116c6793b6ace414cd25b11148c  rpc/broadcaster/service_status.go
672aea7b9f7015f490d56d8b8a799c5d88ba633ca768770fe417e0fded32a392  rpc/broadcaster/preparation_capability.go
0661aeefb0d8b8b9dd06ac1a89c381ca513370e8e015f8d13bec34325e472d72  rpc/chain/broadcasters.go
d4df56473f0abc3f2d041cd78bebd75c91341a8a82e6468417121ed5b339aea3  rpc/broadcaster/fee_lock_test.go
5f734c93c21aa7219f8a7fa748b2b6690b95997f8af48536a5d6e1e852cb6a03  rpc/broadcaster/preparation_capability_test.go
55cefb4f2341a94536390022cf67bdcaa7d1aa0cb0ba8dad1b95708d69ce1350  rpc/chain/broadcaster_initialization_test.go
```

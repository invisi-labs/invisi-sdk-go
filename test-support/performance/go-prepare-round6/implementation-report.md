# Go initialization capability implementation report

Production changes form the frozen candidate. Independent review is GREEN with no blocker; see [review-report.md](review-report.md). This report describes implementation and verification completed by `/root/go_capability_init`; final timing conclusions belong to the root report.

## Design and API

`broadcaster.Client.InitializeTokenSupport(ctx, token, trust, valid)` is the explicit initialization API. It consumes `broadcaster_getTokenSupport` once, validates its embedded service status and token identity, and privately captures a copied capability for that exact client. The actual server contract was verified in `/Users/wx/Workspace/invisi-lab/invisi-broadcaster/internal/service/api.go`: `TokenSupport` embeds `ServiceStatus`, and `GetTokenSupport` constructs its result using `a.GetStatus()`. Consequently, initialization needs no extra `broadcaster_getStatus` RPC and does not infer capability absence from a partial token-support schema.

The raw response decoder rejects a present null or malformed fee-lock capability. Service validation checks API/protocol versions, chain, protocol address, registered signer/recipient, supported capability version/model, and enabled scheduling bounds. Missing fee-lock capability retains legacy pricing; advertised unsupported/malformed capability and fee-lock failures never cause fallback pricing.

The public low-level initialization method is an explicit caller lifetime opt-in: its validity callback does not mint native chain authority. The chain integration supplies the native discovery receipt guard described below.

The copied capability stores deployment trust as immutable chain text and address values, copies the fee-lock descriptor, and captures the client configuration and caller-provided receipt validity guard. A private shared authority owns a mutex, generation, original-client identity and current capability pointer. It contains no process-global cache. Explicit initialization retires previous authority at admission, before network IO, including canceled and failed refreshes. Only the admitted generation may publish its response. IO and receipt callbacks execute outside authority locks.

`ForPreparation` reuses authority only for its exact original client/configuration and matching deployment trust. Uninitialized clients retain a fresh status probe on every preparation. Revoked initialized handles fail closed and require explicit initialization again. Fee-lock-enabled preparation still makes a new `broadcaster_lockFee` call for each operation and retains existing cryptographic signature, token, deployment, model, recipient, expiry and quote-bound verification. Scoped pricing clients carry their original receipt pointer and share revocation with the original client; reinitializing the original cannot restore an old scoped client.

## Discovery lifetime and observed reads

`chain.Protocol.InitializeBroadcasters` binds each initialized client to its discovery receipt. The guard checks the published claim identity, RPC discovery source, native provider identity and HTTP binding epoch, and exact HTTP transport/jar/timeout policy. Implicit HTTP policies also capture the actual `http.DefaultTransport` identity. Refresh admission, eviction, failed or canceled refresh, supersession, and observed policy/source/epoch changes retire old authority. Once a policy or source mismatch is observed, changing it back does not restore the old capability.

An ordinary `GetBroadcasters` call during a pending initialization uses ordinary discovery without revoking the unpublished initializer. A revoked claim cannot subsequently publish. Fresh discoveries outside the owned, cacheable initialized path do not mint reusable capability.

Live `ServiceStatus`, `SupportsToken` and `SubmissionRequirements` calls capture the current authority pointer before IO. A failed, canceled, malformed, mismatched, halted or changed fee-lock status retires only that captured authority. An older response cannot revoke a newer initialization. Successful unchanged live status preserves capability reuse for the next preparation. Plain `SupportsToken` remains a fresh probe and never mints reuse authority.

Fee-lock capability describes pricing, not submission authorization. A changed live fee-lock descriptor retires future preparation and scoped pricing authority; an already-signed quote may still submit when its existing signature/deployment checks and live halt/provenance requirements pass. `SubmissionRequirements` remains a fresh `broadcaster_getStatus` request. This change intentionally does not reject an otherwise valid signed quote merely because future tariff capability changed.

## Changed files

Files edited for this implementation:

- `rpc/broadcaster/client.go`
- `rpc/broadcaster/fee_lock.go`
- `rpc/broadcaster/service_status.go`
- `rpc/broadcaster/fee_lock_test.go`
- `rpc/chain/broadcasters.go`
- `rpc/chain/broadcaster_initialization_test.go` (preexisting untracked test extended)

New files:

- `rpc/broadcaster/preparation_capability.go`
- `rpc/broadcaster/preparation_capability_test.go`

`rpc/chain/broadcasters.go` already contained uncommitted discovery optimizations when work began; those were preserved. Other preexisting working-tree changes, including `rpc/chain/protocol.go`, were outside this implementation's edits. No commit, rebase or push was performed.

## RED evidence

These failures were observed before their corresponding production changes. They are preserved in the tool transcript; separate RED log files were not written.

1. `go test ./rpc/broadcaster -run TestInitializedPreparationCapabilityReuseAndRevocation -count=1` failed with `explicit token support preparation initialization unavailable`, establishing the missing initialization API and reuse behavior.
2. `go test ./rpc/chain -run TestExplicitBroadcasterInitializationReusesOnlyOwnedBindingEpoch -count=1` failed with `unexpected method broadcaster_getStatus` and `initialized capability was not reused invalid broadcaster service status`, establishing the unwanted per-preparation probe.
3. The same chain test, extended for an ordinary lookup overlapping initialization, failed with `ordinary lookup revoked in-flight initialization broadcaster discovery receipt retired`. The fix preserves unpublished claims and prevents revoked publication.
4. `go test ./rpc/broadcaster ./rpc/chain -run 'TestInitializedPreparationCapabilityReuseAndRevocation|TestBroadcasterInitializationPolicyTracksDefaultTransport' -count=1` failed independently with `fresh token support observation retained stale downgrade authority` and `default transport replacement retained initialized HTTP policy`. The fixes add observed-read invalidation and bind implicit transport identity.
5. The chain initialization test, extended for discovery-source replacement, failed with `changed discovery source retained old client authority`. The guard now binds the exact discovery RPC client.

Additional regressions exercise stale overlapping initializations, old live failures arriving after newer initialization, canceled refresh admission, malformed/unsupported capability initialization, per-operation fresh enabled fee locks, unchanged live submission checks, shared scoped-client revocation, failed/canceled chain refreshes, superseded client handles, policy replacement and restoration, native epoch invalidation, and discovery source replacement and restoration.

## Focused verification

Working directory for all commands:

`/Users/wx/Workspace/invisi-lab/invisi-sdk-go/.worktree/submit-read-audit`

Final focused command:

```sh
go test ./rpc/broadcaster ./rpc/chain -count=1 > test-support/performance/go-prepare-round6/go-capability-focused.log 2>&1
```

Result: PASS, broadcaster 1.646s and chain 2.327s. Log: [go-capability-focused.log](go-capability-focused.log).

Final focused race command:

```sh
go test -race ./rpc/... -count=1 > test-support/performance/go-prepare-round6/go-capability-rpc-race.log 2>&1
```

Result: PASS, with no race reports:

- `rpc`: 5.622s
- `rpc/broadcaster`: 3.061s
- `rpc/chain`: 4.909s
- `rpc/contracts`: 2.804s
- `rpc/provenance`: 3.393s

Log: [go-capability-rpc-race.log](go-capability-rpc-race.log).

The focused checks above preceded independent review. After review GREEN, the root agent explicitly requested the heavy checks below. No timed diagnostic was run by this implementation agent. The root agent owns the frozen before/after diagnostic, timing conclusions, README and final verification orchestration. No production or test files changed during heavy verification or this report update.


## Post-review heavy verification

Both commands ran sequentially against the frozen candidate from the working directory recorded above. Both exited successfully with code `0`.

```sh
python3 scripts/test-modules.py > test-support/performance/go-prepare-round6/module-checks.log 2>&1
python3 scripts/check-packages.py > test-support/performance/go-prepare-round6/archive-checks.log 2>&1
```

The full 9-module check passed module boundaries plus `go test -race ./...`, `go vet ./...` and `go build ./...` for primitives, protocol, prover, rpc, root SDK, session, ipp, paymentrouter and examples. [module-checks.log](module-checks.log) SHA-256:

```text
d7b2751f109f5ac7dcae5275231a5d033e00483812f0e0e6d7e15fbd3058dddd
```

The full 8-archive check passed independent extracted module consumers, each with `go test ./...`, `go vet ./...` and `go build ./...`, including package-license and prover-size checks performed by the script. [archive-checks.log](archive-checks.log) SHA-256:

```text
16ed55ad6bc6a2993d740215f43f0580cca44fc81f185ab5c57ca5193b1d049d
```

Archive evidence directory retained by the checker:

`/var/folders/kj/xt7gv9hn5f76zmw7vqdpl36m0000gn/T/invisi-go-package-check-zj4nv5c0`

Heavy verification is complete (`HEAVY_DONE`). There are no observed test, race, vet, build, boundary or archive-check failures in these runs. No source/test edits, timing runs, commits, rebases or pushes were performed during this verification phase.

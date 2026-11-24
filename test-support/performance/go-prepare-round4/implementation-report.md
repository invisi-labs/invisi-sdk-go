# Go Shield initialization

## Result and API

An empty managed wallet on an already validated native chain binding performs three chain reads during explicit Shield initialization. These are the latest header, a live network check, and token code at the canonical hash. Ordinary service clients also issue a fresh protocol-service deployment request. The resulting count is four, down from six for the same deployment with no address registry. With a live constructor and `protocolrpc.ClientOptions{UseNativeHTTPTransport: true}`, the session can reuse the constructor's authenticated service lookup, reducing the count to three. Counts in this section come from focused local HTTP regression tests. The project-level before/after trace and timing comparison use the separate diagnostic.

The option selects a private SDK-owned `net/http.Transport` with pooled connections, environment proxy support, standard TLS and the existing redirect refusal. The client copies caller-supplied headers. Combining this option with `HTTPClient` returns an error. Existing default and caller-supplied HTTP clients preserve their transport policy and always contact the service when explicitly prewarming. The optional field is additive for keyed `ClientOptions` literals. Go callers with unkeyed literals must update them for the changed struct shape.

## Evidence lifetimes

* Core code hash, VERSION and the optional immutable address-registry binding use the existing receipt on the exact `chain.Protocol`, `*chain.HTTPBinding` and epoch. `ValidateDeployment` always calls `ValidateNetwork` on that native binding. A miss reads and validates the immutable deployment at latest. The new historical capture contains the genuine head and token evidence. It makes no historical Core observation at that head. Full wallet sync continues to populate its offline snapshot using block-pinned recorder reads.
* `ProtocolRpcClient.VerifyDeployment` performs an actual service lookup, validates the response and compares every deployment field including optional registries. It returns a process-local `DeploymentObservation`. `Current` checks the shared deployment-request generation. `Reusable` also requires the selected SDK-owned service transport. Starting another deployment lookup on the client or any provider clone retires earlier observations even if the new lookup fails or is cancelled. A successful overlapping lookup can complete normally while its older observation is already retired. This preserves ordinary concurrent discovery and prevents delayed responses from minting preparation authority.
* `ResolveRuntime` remembers the constructor's observation and the native epoch sampled before its deployment checks. Initialization reuses the observation only for the same runtime RPC clone, protocol, concrete provider, matching epoch and owned service policy. `WithProvider` copies configuration and shares revocation with its originating service client. It cannot create a successful observation from deployment pins. `RestorePreparationRuntime` supplies no constructor observation. A native epoch change or an independently selected service client forces fresh service authentication. After retirement or offline restoration, further explicit captures continue checking the service until the wallet has a new live constructor.
* Full sync verifies the selected service whenever it promotes newly read token evidence. The managed Shield receipt fences the service observation during publication, use, guarded reservation and first-send checks. The existing capture ticket still governs concurrent wallet refresh and initialization. A failed replacement cannot restore the former historical token receipt. Checks on wallet generation, reset revision, checkpoint identity, the native epoch, token, and context remain active.
* Token code is read per capture. EIP-1898 capability fallback performs the existing height read and final header fence. Actual wallet checkpoints can require additional reads. Nonces and payer/signature policy remain current. Journal version 2, serialized recovery, post-freeze first-send fencing, uncertain retries of identical packets, and known-ID status reads keep their prior paths.

## Verification

The first regression run against the frozen production source observed five chain requests plus a service lookup on native prewarm. The same run reproduced publication after a fresh service lookup had failed during the token read. A second failing regression showed that rejecting a matching, superseded request at the RPC layer would needlessly break ordinary overlapping discovery. The fix accepts the matching response while the generation check permanently prevents using its old observation as a capability.

Regression coverage includes exact native/default/custom/epoch request counts, option-pin plus offline restore against an unavailable selected service, immutable header copying, custom-policy rejection, public fresh lookup, overlapping success after newer failure, cancellation through a provider clone, and an original-client failure during token capture. Existing focused tests exercise shared-binding wrong-network retirement, full-sync service authentication, checkpoint fences, failed/superseded refresh, retained journal and first-send behavior. Full module, release-archive and timed diagnostic results belong to the project verification report.

The final focused command passed on both selected packages. Go reported 1.705 seconds for RPC and 12.509 seconds for session. These are test-run durations.

```sh
go test -race ./rpc ./session -run 'Test(ServiceDeploymentObservation|RestoredPrewarm|ShieldPrewarm|DeploymentRefreshFailure|InitializedShield|SynchronizedShield|RestoredWalletRefreshAuthenticatesActualProtocolService|NativeCaptureWrongNetworkRetiresSharedBindingEpoch|RetiredShieldAnchor|FailedNativeRefresh|NativeRefresh|ActiveWalletScan|RetainedShieldJournal)' -count=1
```

## Independent review corrections

Three regressions reproduced the review findings on the initial candidate. An earlier initializer resumed after its metadata read and retired the newer successful service observation (three total service calls instead of two). A full refresh that committed its wallet state returned a service error when a newer initializer won before publication. An empty wallet accepted a header below the authenticated Core deployment floor and constructed a retained Shield with an explicit nonce.

A mutex shared by session copies orders replacement of the wallet capture ticket, admission of constructor reuse, and reservation of a fresh service-request generation. `BeginDeploymentVerification` reserves the generation synchronously and returns a one-shot lookup for execution after the mutex is unlocked. Full sync uses the same admission check and can finish its independent wallet work after losing the optional capture. Publication samples service validity before checking ownership, giving a superseded wallet ticket precedence. It checks ownership again after reading wallet state and before atomic publication.

Explicit initialization now compares the captured height with `Protocol.FromBlock` after validating the live network. Heights below deployment fail before any token-code proof or retained capability can be published. A wrong network still retires the shared native epoch. The three new regressions passed after these changes.

The repaired candidate passed `go test -race ./rpc ./session` with the earlier focused expression extended by `SupersededShield|ShieldCaptureRejectsPredeploymentHead`, using `-count=1`. Go reported 1.967 seconds for RPC and 13.137 seconds for session. `git diff --check` passed.

## Full candidate verification

The independent review cleared both findings. The following commands ran sequentially from the release worktree on the frozen candidate. Both exited with status 0.

```sh
python3 scripts/test-modules.py > test-support/performance/go-prepare-round4/module-checks.log 2>&1
python3 scripts/check-packages.py > test-support/performance/go-prepare-round4/archive-checks.log 2>&1
```

The first script verified module boundaries, then ran `go test -race ./...`, `go vet ./...` and `go build ./...` in primitives, protocol, prover, rpc, the root SDK, session, ipp, paymentrouter and examples. Go reported 137.978 seconds for the session package's race suite. Some unchanged packages reused Go's test cache. The second script packaged the eight public modules into a local proxy, independently downloaded and extracted each archive, and ran `go test ./...`, `go vet ./...` and `go build ./...` in each extracted consumer.

| Log | SHA-256 |
| --- | --- |
| [Module verification](module-checks.log) | `1a827d5c84a3add361f0d8baa037bfe760dda339bc222bef21c71d80cc27da5a` |
| [Archive verification](archive-checks.log) | `b0d6c6210916740143885b90ee4e51058d34a0e6229730d4f2b82082d92428e0` |

Archive evidence directory: `/var/folders/kj/xt7gv9hn5f76zmw7vqdpl36m0000gn/T/invisi-go-package-check-zjjqhdj4`. Heavy verification finished before the serial timing run. The HEAVY_DONE handoff was issued at 2026-11-24 17:31:00 UTC.

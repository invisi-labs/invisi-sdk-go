# Three SDK stage boundaries

The transaction pipeline now carries prepared business/funding facts forward instead of rediscovering them during quotation or building missing Source evidence during submission. The TypeScript post-persistence send fence was repaired first; equivalent final authority checks and durable recovery were then reviewed across all three SDKs.

## Stage contract

| Stage | Responsibility |
|---|---|
| Initialize | Resolve and validate reusable deployment, provider, token and broadcaster capabilities within their actual lifetime and policy scope. |
| Prepare | Resolve business inputs/recipients and select this operation's current funding nonce. Explicit zero remains an exact value. |
| Quote | Obtain authenticated pricing and reprice the already selected plan locally. No nonce selection, general preparation, new input selection or chain reads. Insufficient budget/prerequisites explicitly require preparation again. |
| Prove / sign | Produce the main proof and attach the final funding signature. Required-Source private operations remain awaiting explicit finalization. Signing never generates a Source proof. |
| Ready / finalize | Generate and validate Source evidence against the final signed native payload, then publish a complete ready artifact atomically. Failure retains the main proof, signature and reservations. No-Source automatic readiness remains compatible. |
| Submit | Validate the frozen packet and necessary live requirements, persist the exact first handoff, and check current authority at the actual SDK transport boundary. No proving, signing, nonce selection or general preparation. |
| Recover | Replay the original packet through the original client when its ID is unknown; query status only when the ID is known. No fresh quote, signature or proof. |

Rust preserves the raw `FundingParams` ABI, including exact nonce zero and caller funding. Its additive `SignatureFundingNonce::{Current, Exact, Verified}` preparation APIs express the same prerequisite selection as TS/Go. Recursive Rust operations may retain their native `Proved` phase while a separately finalized envelope controls submission; a recursive proof is never exported as EVM calldata.

Fee repricing retains selected nonce/deadline, funding mode, business outputs, grant/schedule identity and final-gross permit requirements. It can adjust prepared change within the selected private inputs, but cannot silently add another note or reinterpret caller funding as signature funding. Restored plans missing required recipient keys or evidence fail explicitly instead of expanding quote/submit into preparation.

## Safety and recovery

The TS final guard runs after awaited persistence and covers provider/reader policy, Source trust, wallet generation, worker, attempt token, cancellation and reservations. SDK-stock encoding loads before this final interval; no additional await separates the final synchronous check from stock transport handoff.

Go and Rust now also observe relevant SDK wallet writes across their final asynchronous checks. Regressions cover worker takeover, invalidation, generation changes during the last network RPC, same-chain provider replacement and native reorganization/archive writes. No observer registry lock spans network or storage I/O. Observers conservatively reject overlapping or aborted writes.

A final authority refusal after durable externalization preserves the exact packet and reservations. This remains distinct from an authenticated, known pre-admission execution refusal whose existing rollback rules allow a fresh preparation. Source-ready failure retains the main proof; attempted recovery never rebuilds evidence.

Observation applies to the backend/scope identity the SDK can actually share. Raw writes bypassing a custom adapter and independent-process writes without notifications remain bounded by the latest durable snapshot; guarantees for independently opened database connections vary by the native adapter. Custom transports own waits they add after SDK handoff. No unsupported atomic guarantee is inferred for those boundaries.

## Paired diagnostic

The before snapshots contain all pre-existing optimization work, not the original screenshot's code. Their source manifests were captured before this refactor and checked for zero changes. Disposable baseline test copies overlay only common diagnostic fixtures/drivers; no baseline production file is changed. Source, overlay, driver and native binary fingerprints accompany the traces.

Ordinary flow: signature-funded Shield, amount 30, fee 5, cap 10, embedded recipient, public test payers 01/02 with live nonces 7/14. Each of three fixtures reuses one initialized session/store for two transactions and verifies that the first journal remains intact. Both variants omit the nonce at the public preparation boundary. The Rust API adapter uses legacy raw preparation before and explicit `Current` preparation after.

Chain/relay reads use actual loopback HTTP with 20 ms per response. Ordinary broadcast is denied at the innermost transport: its counts are **pre-broadcast HTTP reads**, and its submit time ends at that denial. A real broadcast would add its own request and service latency. Cold setup/initialization and review dwell are separately recorded; build/process startup is excluded.

Private flow: one 100-unit Note, fixed output 30, fee 5, cap 10, no public funding. Both main and Source provers are counted test backends, not real ZK generation. Signing, authenticated certificates/manifest, statement/packet bindings and exact replay checks still run. Source proof movement is measured separately from end-to-end cost.

Private instrumentation deliberately stays distinct: Go routes chain, protocol, Source and relay through HTTP; TS routes Source/relay through HTTP and counts chain/protocol reader calls separately; Rust routes Source through HTTP and counts chain/relay facade calls separately. Private totals and absolute times must not be compared between languages. Per-language before/after drivers are identical.

`measure.py` runs variants sequentially after builds/tests finish. `summarize.py` computes stage medians and preserves count ranges rather than treating facade calls as HTTP. Raw JSON retains request methods and parameters.

## Measured ordinary flow (20 ms HTTP)

| SDK / variant | HTTP first / repeat | Hot path ms first / repeat | Cold setup + init HTTP | First use total HTTP |
|---|---:|---:|---:|---:|
| TS before | 43 / 41 | 836.52 / 811.53 | 20 | 63 |
| TS after | 8 / 8 | 247.92 / 258.71 | 20 | 28 |
| GO before | 7 / 7 | 181.92 / 184.03 | 13 | 20 |
| GO after | 7 / 7 | 191.46 / 188.40 | 13 | 20 |
| RUST before | 7 / 7 | 1698.58 / 1714.70 | 10 | 17 |
| RUST after | 7 / 7 | 1777.29 / 1805.63 | 10 | 17 |

The HTTP totals end at denied broadcast. No actual ordinary submit request is sent. Three fixtures per variant; timing is the median of per-operation stage sums, not a sum of stage medians. Cold/init model differs by adapter and is not a cross-language ranking. Ranges and individual samples remain in `results/ordinary-totals.json` and raw traces.

### Ordinary phase counts and medians

| SDK / variant | Transaction | Stage | HTTP | Median ms |
|---|---:|---|---:|---:|
| TS before | 1 | fundingNoncePrefetch | 0 | 0.00 |
| TS before | 1 | prepareShield | 6 | 154.53 |
| TS before | 1 | broadcasterDiscovery | 0 | 0.07 |
| TS before | 1 | quote | 5 | 117.28 |
| TS before | 1 | fundingAuthorizationAndSignature | 16 | 286.15 |
| TS before | 1 | submit | 16 | 280.29 |
| TS before | 2 | fundingNoncePrefetch | 0 | 0.00 |
| TS before | 2 | prepareShield | 5 | 130.80 |
| TS before | 2 | broadcasterDiscovery | 0 | 0.00 |
| TS before | 2 | quote | 4 | 116.48 |
| TS before | 2 | fundingAuthorizationAndSignature | 16 | 284.96 |
| TS before | 2 | submit | 16 | 281.18 |
| TS after | 1 | fundingNoncePrefetch | 0 | 0.00 |
| TS after | 1 | prepareShield | 2 | 56.42 |
| TS after | 1 | broadcasterDiscovery | 0 | 0.06 |
| TS after | 1 | quote | 2 | 66.44 |
| TS after | 1 | fundingAuthorizationAndSignature | 0 | 14.63 |
| TS after | 1 | submit | 4 | 111.16 |
| TS after | 2 | fundingNoncePrefetch | 0 | 0.00 |
| TS after | 2 | prepareShield | 2 | 56.91 |
| TS after | 2 | broadcasterDiscovery | 0 | 0.00 |
| TS after | 2 | quote | 2 | 66.83 |
| TS after | 2 | fundingAuthorizationAndSignature | 0 | 18.04 |
| TS after | 2 | submit | 4 | 114.15 |
| GO before | 1 | noncePrerequisite | 0 | 0.00 |
| GO before | 1 | prepare | 2 | 51.85 |
| GO before | 1 | discoveryLookup | 0 | 0.00 |
| GO before | 1 | quote | 2 | 48.04 |
| GO before | 1 | fundingAuthorizationAndSign | 0 | 14.57 |
| GO before | 1 | submitToDeniedBroadcast | 3 | 67.71 |
| GO before | 2 | noncePrerequisite | 0 | 0.00 |
| GO before | 2 | prepare | 2 | 50.70 |
| GO before | 2 | discoveryLookup | 0 | 0.00 |
| GO before | 2 | quote | 2 | 48.30 |
| GO before | 2 | fundingAuthorizationAndSign | 0 | 16.36 |
| GO before | 2 | submitToDeniedBroadcast | 3 | 69.02 |
| GO after | 1 | noncePrerequisite | 0 | 0.00 |
| GO after | 1 | prepare | 2 | 55.75 |
| GO after | 1 | discoveryLookup | 0 | 0.00 |
| GO after | 1 | quote | 2 | 50.54 |
| GO after | 1 | fundingAuthorizationAndSign | 0 | 16.46 |
| GO after | 1 | submitToDeniedBroadcast | 3 | 67.98 |
| GO after | 2 | noncePrerequisite | 0 | 0.00 |
| GO after | 2 | prepare | 2 | 51.98 |
| GO after | 2 | discoveryLookup | 0 | 0.00 |
| GO after | 2 | quote | 2 | 48.31 |
| GO after | 2 | fundingAuthorizationAndSign | 0 | 17.72 |
| GO after | 2 | submitToDeniedBroadcast | 3 | 69.64 |
| RUST before | 1 | prepare | 0 | 1467.56 |
| RUST before | 1 | quote | 4 | 106.69 |
| RUST before | 1 | funding-signature | 0 | 24.24 |
| RUST before | 1 | submit | 3 | 99.88 |
| RUST before | 2 | prepare | 0 | 1456.59 |
| RUST before | 2 | quote | 4 | 111.13 |
| RUST before | 2 | funding-signature | 0 | 37.62 |
| RUST before | 2 | submit | 3 | 107.91 |
| RUST after | 1 | prepare | 2 | 1578.18 |
| RUST after | 1 | quote | 2 | 55.78 |
| RUST after | 1 | funding-signature | 0 | 31.36 |
| RUST after | 1 | submit | 3 | 108.76 |
| RUST after | 2 | prepare | 2 | 1577.22 |
| RUST after | 2 | quote | 2 | 62.72 |
| RUST after | 2 | funding-signature | 0 | 47.46 |
| RUST after | 2 | submit | 3 | 123.20 |

## Measured private Source flow

| SDK / variant | Stage | Actual HTTP | Chain facade calls | Relay facade calls | Source proofs | Median ms |
|---|---|---:|---:|---:|---:|---:|
| TS before | prepare | 0 | 10 | 0 | 0 | 142.58 |
| TS before | quote | 3 | 3 | 0 | 0 | 189.11 |
| TS before | authorize | 0 | 0 | 0 | 0 | 21.00 |
| TS before | mainProve | 0 | 0 | 0 | 0 | 21.38 |
| TS before | ready | 0 | 0 | 0 | 0 | 0.09 |
| TS before | submit | 5 | 11 | 0 | 1 | 289.46 |
| TS before | restore | 0 | 0 | 0 | 0 | 10.97 |
| TS before | replay | 1 | 0 | 0 | 0 | 57.13 |
| TS before | knownIDStatus | 1 | 0 | 0 | 0 | 32.97 |
| TS after | prepare | 0 | 10 | 0 | 0 | 144.85 |
| TS after | quote | 3 | 0 | 0 | 0 | 172.32 |
| TS after | authorize | 0 | 0 | 0 | 0 | 21.17 |
| TS after | mainProve | 0 | 0 | 0 | 0 | 19.32 |
| TS after | ready | 3 | 2 | 0 | 1 | 187.06 |
| TS after | submit | 2 | 10 | 0 | 0 | 111.55 |
| TS after | restore | 0 | 0 | 0 | 0 | 11.66 |
| TS after | replay | 1 | 0 | 0 | 0 | 58.23 |
| TS after | knownIDStatus | 1 | 0 | 0 | 0 | 32.56 |
| GO before | initialize | 9 | 0 | 0 | 0 | 196.34 |
| GO before | prepare | 17 | 0 | 0 | 0 | 370.87 |
| GO before | fixtureSetup | 1 | 0 | 0 | 0 | 24.97 |
| GO before | quote | 4 | 0 | 0 | 0 | 105.09 |
| GO before | authorize | 0 | 0 | 0 | 0 | 4.09 |
| GO before | mainProve | 0 | 0 | 0 | 0 | 10.43 |
| GO before | ready | 0 | 0 | 0 | 0 | 0.00 |
| GO before | submit | 14 | 0 | 0 | 1 | 340.80 |
| GO before | restore | 0 | 0 | 0 | 0 | 13.55 |
| GO before | replay | 1 | 0 | 0 | 0 | 28.64 |
| GO before | status | 1 | 0 | 0 | 0 | 22.02 |
| GO after | initialize | 9 | 0 | 0 | 0 | 205.38 |
| GO after | prepare | 17 | 0 | 0 | 0 | 375.71 |
| GO after | fixtureSetup | 1 | 0 | 0 | 0 | 24.35 |
| GO after | quote | 4 | 0 | 0 | 0 | 102.77 |
| GO after | authorize | 0 | 0 | 0 | 0 | 3.40 |
| GO after | mainProve | 0 | 0 | 0 | 0 | 10.86 |
| GO after | ready | 4 | 0 | 0 | 1 | 100.16 |
| GO after | submit | 11 | 0 | 0 | 0 | 259.66 |
| GO after | restore | 0 | 0 | 0 | 0 | 12.88 |
| GO after | replay | 1 | 0 | 0 | 0 | 29.08 |
| GO after | status | 1 | 0 | 0 | 0 | 21.93 |
| RUST before | initialize | 0 | 5 | 0 | 0 | 1648.96 |
| RUST before | prepare | 0 | 5 | 0 | 0 | 1594.64 |
| RUST before | quote | 0 | 2 | 0 | 0 | 1605.76 |
| RUST before | prove | 0 | 0 | 0 | 0 | 82.02 |
| RUST before | ready | 0 | 0 | 0 | 0 | 0.01 |
| RUST before | firstSubmit | 3 | 3 | 1 | 1 | 630.81 |
| RUST before | unknownIdReplay | 0 | 0 | 1 | 0 | 178.87 |
| RUST before | knownIdStatus | 0 | 0 | 1 | 0 | 82.25 |
| RUST after | initialize | 0 | 5 | 0 | 0 | 1565.15 |
| RUST after | prepare | 0 | 5 | 0 | 0 | 1512.98 |
| RUST after | quote | 0 | 0 | 0 | 0 | 1497.54 |
| RUST after | prove | 0 | 0 | 0 | 0 | 79.90 |
| RUST after | ready | 3 | 0 | 0 | 1 | 528.06 |
| RUST after | firstSubmit | 0 | 3 | 1 | 0 | 145.97 |
| RUST after | unknownIdReplay | 0 | 0 | 1 | 0 | 140.34 |
| RUST after | knownIdStatus | 0 | 0 | 1 | 0 | 90.74 |

Source proof count moves from submit to ready in all variants. TS private quote removes 3 chain-reader calls; Rust private quote removes 2 chain-snapshot calls; Go quote already has no chain HTTP. Private Source/relay instrumentation differs by SDK as described above: these are per-language paired results, not cross-language HTTP totals. Main/Source proof engines are fixtures; this table does not measure real ZK latency.

Go private normal-stage HTTP increases from 35 to 36 (prepare + quote + ready + submit), because the final post-persistence network check is now also performed on this conservative path. Its submit moves from 14 to 11 HTTP calls; ready adds 4. TS private total HTTP remains 8; its submit moves from 5 to 2 while ready adds 3. Rust Source HTTP remains 3 and moves wholly to ready. These are responsibility changes, not claims that moving proof work eliminates its end-to-end cost.

Rust measurement targets are isolated by variant. Each local SDK crate was explicitly cleaned and rebuilt; dependency caches alone were reused. The initial shared-target Rust samples were superseded and excluded from the final dataset.

## Verification

- TS: all ten packages verified, 1,892 passed and 42 conditional skips. Full build, type checks, public ESM/CJS surface, session/module boundaries, WASM and formatting checks passed. Native proof/live-only conditional tests remain skipped.
- Go: all nine modules passed race/test/vet/build; all eight independently extracted module archives passed test/vet/build.
- Rust: full workspace/all-features passed, 517 passed and 17 ignored across 99 result groups. Clippy, formatting, boundaries and English checks passed. Explicit native proving resource tests remain ignored.
- Independent scoped reviews and the final cross-SDK semantic review are GREEN at TS 42, Go 55 and Rust 21 task paths. The initial failing race tests, fixes and full-suite fixture migrations are retained in the evidence.

Inherited TS gates remain explicitly unresolved: the eager browser entry is 1,294,233 bytes against a 1 MiB budget (before: 1,329,770); the older generated `three-sdk-alignment/REPORT.md` contains Chinese text and fails the English-only lint. Neither threshold nor lint policy was relaxed. This report does not claim remote CI is green.

No commits, rebases, pushes, releases, deployments or public-chain broadcasts were performed.

## Evidence and reproduction

`results/` contains the final 24 trace JSON files, per-stage and total summaries, source/build fingerprints, commands, validation logs and independent review reports. `verification.json` confirms frozen source hashes, identical paired drivers, isolated Rust binaries, no chain reads in quote, Source proof placement and exact replay/status behavior. Native binaries and full source snapshots remain under `/tmp/invisi-sdk-stage-boundaries-20261125`; they were not committed.

Recompute the compact summary with `python3 summarize.py /absolute/evidence-directory` and verify the measured contracts with `python3 verify.py /absolute/evidence-directory`. `measurement-commands.json` records each final invocation. `measure.py` requires the prepared source snapshots/native binaries described by their build records; it never provisions or broadcasts to a public chain.

The zero-delay smoke counts are not mixed into final 20 ms samples. TS before has 38/35 reads at zero delay versus 43/41 at 20 ms: the stock provider cache makes request counts timing-dependent on that older default path. After remains 8. Go/Rust ordinary counts remain 7; their small three-sample timing increases are not presented as improvements. Rust debug time remains dominated by local cryptographic preparation.

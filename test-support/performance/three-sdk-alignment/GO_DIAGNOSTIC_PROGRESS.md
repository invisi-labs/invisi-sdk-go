# Unified Go HTTP diagnostic progress

Harness: `session/three_sdk_http_diagnostic_test.go`; source-compatible provider factories select real frozen-before `ethclient` and aligned-after owned `chain.HTTPBinding` through the `three_sdk_before` build tag. The common harness feature-detects the optional explicit nonce and broadcaster initialization APIs. Before preparation performs its original nonce read; after uses the selected fresh nonce directly as `FundingSource.SigNonce`. Different nonzero fixture nonces (7,14) prevent silently relying on zero.

Three fixtures each create two public synthetic wallets (01/02 repeated bytes), one protocol, one session and one stock broadcaster client reused across both Shield operations. Both operations target the embedded long receiving address, amount 30, fee 5, cap 10. The first operation/journal remains intact. Every phase counts actual localhost chain/protocol/broadcaster HTTP attempts at the real handler. Artificial delay is 20 ms per actual response; opt-in zero delay exists for functional verification only. Native chain binding is preserved. The identical diagnostic relay RoundTripper in both variants denies a write immediately before forwarding to native transport. No production write-guard API is required; the concrete stock broadcaster client still performs its normal signed quote, intent, endpoint and service checks.

Cold setup and actual discovery/support initialization have distinct phase rows and are charged once per fixture. Flow phases are fresh nonce prerequisite, prepare, discovery lookup, quote, authorization/signature and submit through innermost denied broadcast. Executable pre-main macOS loader delays, compilation and test startup are outside all measured timers. First and repeat transaction groups remain separate.

`prepare_go_diagnostic.py --frozen <frozen-go-directory> --out <new-directory>` verifies all 701 frozen text source files byte-for-byte, adds only diagnostic tests and unchanged embedded JSON assets, freezes after sources separately, and compiles both binaries. It writes source hash manifests and exact commands to `RUN_AFTER_CPU_QUIET.txt`. It never runs delayed timing groups. `summarize_go_diagnostic.py <directory>` produces readable `REPORT.md` and `summary.json` from both raw outputs.

Functional attempts: compilation succeeds for frozen before. First zero-delay execution exposed a fixture URL missing `/v1`, fixed in harness. macOS executable startup repeatedly waits minutes before test main; no startup delay is reported as SDK time. Final functional verification and controlled delayed sampling pending; do not treat earlier v1-v4 binaries as final.

Native owner owns failure-recovery business regressions; harness intentionally does not duplicate those tests. References: `TestFrozenJournalWriteFailureLeavesCompleteUnexternalizedState`, `TestFrozenJournalRestartReplaysIdenticalPacketThenUsesKnownIDStatus`, `TestFrozenJournalCrashAfterAtomicCommitBeforeSendCanResume`, `TestCustomExternalizationPolicyStillBlocksBroadcasterSend`, `TestPlainShieldQuoteUsesPaidEstimateThenExactSignedQuote`, `TestPlainShieldQuoteRefreshRequiresRealIncreaseWithinCap`, `TestOrdinaryStructuredFirstRefusalRollsBackButAmbiguityStaysFrozen`, chain nonce/binding and broadcaster initialization tests.

## Handoff at root-requested pause

No delayed samples have run. All diagnostic processes have now exited (last process inventory found none).

- `/tmp/invisi-go-unified-http-20261124-v5/before.test`: zero-delay functional PASS, 3 fixtures × 2 transactions, 0.35 s in test main; raw `/tmp/invisi-go-unified-http-20261124-v5/before-functional.json`. Process PID 33235 / tool session 62605 exited 0. All 701 original frozen source files verified unchanged during preparation.
- `/tmp/invisi-go-unified-http-20261124-v5/after.test`: zero-delay fixture 1 PASS; fixtures 2/3 failed old native HTTP constructor default-transport validation (`native HTTP transport required`). Process PID 33234 / tool session 66694 exited 1. This snapshot predates owner's constructor fix to own a fresh native transport; do not sample this binary. Raw `/tmp/invisi-go-unified-http-20261124-v5/after-functional.json` is failed diagnostic evidence only.
- Older v1 before session 80752 / PID 30513 exited 1 (fixture URL missing `/v1`, fixed). Older v3 after session 82735 / PID 31840 exited 1 (same old URL / old constructor). All polled, closed and no longer pending. Compile sessions 30756, 4745, 57091, 9572, 95496, 14934 also completed; v4 after compilation captured transient owner unused-import state and failed, superseded by v5.
- v5 snapshot and hashes: `/tmp/invisi-go-unified-http-20261124-v5/{before,after,source-hashes.json,before-hashes.json,after-hashes.json,RUN_AFTER_CPU_QUIET.txt}`. Before/after source directories are isolated immutable measurement inputs; source-hashes.json includes all701 frozen-original hashes plus exact compiled source/asset hashes. The script now snapshots after sources too, preventing worktree edits during sampling from invalidating binary provenance.

Current harness is slightly newer than v5: with owner's discovery cache policy now permitting stock clients with custom low-level transport, it passes the identical read-only transport through initialization and reuses that exact initialized stock client for both flows. It also byte-compares the first retained journal after the repeat flow. These final harness changes are gofmt'd but not yet compiled or run. No further source edits planned until owner final cache freeze / root follow-up.

Next action on resumption: wait for owner source freeze, run prepare script to a NEW directory (never overwrite v5), run zero-delay functional before and after, notify root ready, then wait for root global CPU-quiet GO before sequential 20 ms before/after groups. Run summary script only on successful delayed raw outputs. MacOS executable pre-main delays were several minutes and remain excluded from phase timers.

## Final candidate v6 prepared and functionally verified

Owner confirmed final source freeze ready. Prepared `/tmp/invisi-go-unified-http-20261124-v6` with all 701 original frozen files verified unchanged, isolated before and after source trees, both compiled successfully. Both zero-delay functional runs PASSED all three fixtures × two flows, exact initialized stock client reused and first journal byte-comparison intact. Process sessions 93278 (before) and 48345 (after) exited 0; no pending diagnostic processes. Root has not authorized delayed timing samples; none have run.

Actual request counts are stable across every first/repeat flow: before 25; after 11. After breakdown: nonce 2 + prepare 4 + discovery 0 + quote 2 + authorization/sign 0 + submit 3. The fourth preparation request is the existing `PersistPlan` canonical-block safety fence (`internal/transaction/complete.go` calls `s.Canonical(ctx, plan.Block)`), in addition to Frame's current network/head/token-code reads. It is actual native SDK work, not a discarded prerequisite or harness duplicate. Cold setup/init actual before 9+2; after 7+3. Zero-delay wall clock values are functional evidence only, not performance results.

- Before binary SHA256: `89eeac2cdc38ba8dbd12acca77e6702ea68ca5f01a82e6aaeb401be69c2160fa`.
- After binary SHA256: `b3d8ec21d41f881eeea1e0750675093db4ba68f2e4604f6f4032b19d2ee1540a`.
- Combined source manifest SHA256: `aee7029008ba766923fe6ff579b654f1afbb54434e10e5dca85b2e30fa047661`.
- Raw functional evidence: v6 `before-functional.json`, `after-functional.json`; compact verification: `functional-verification.json`.
- Source/binary manifests: v6 `source-hashes.json`, `before-hashes.json`, `after-hashes.json`. Every output phase row now includes its populated source/binary manifest; empty hash placeholders are rejected by the harness.
- Exact delayed commands for root's sequential CPU-quiet GO: v6 `RUN_AFTER_CPU_QUIET.txt`. Zero-delay commands: `FUNCTIONAL_COMMANDS.txt`.

Normal driver retains its in-memory native local quote receipt. JSON resume loses the unsigned local quote receipt and conservatively repeats live policy validation; report this distinction alongside separately owned recovery regression results. Do not generalize normal 11-request flow to every resumed operation.

## Final alias candidate v7 ready; awaiting root GO

Go owner explicitly confirmed source stable including `ErrFinalityInconsistent` and the native HeaderByTag identity mappings. v7 isolated after snapshot contains the alias and byte-for-byte matches the owner's frozen tree (all hashed source/assets; no differences). Both independent v7 binaries compiled. All 701 original frozen-before files verified unchanged.

Both zero-delay functional variants PASSED three fixtures × two flows, with exact initialized client reuse and first-journal byte comparison. Each raw report has 42 successful phase rows with populated source/binary manifests. Exact per-flow HTTP counts: before 25 and after 11 across all six flows each; every flow denied exactly one write before outbound HTTP. No delayed samples run. Sessions 80390 (before) and 84450 (after) exited 0. No pending diagnostic processes.

Final evidence root: `/tmp/invisi-go-unified-http-20261124-v7/`.

- Compact validated proof: `functional-verification.json`.
- Raw zero-delay reports: `before-functional.json`, `after-functional.json`.
- Before binary SHA256: `63310b3123affa0a551f3ec429916dd446d8b942d0a78ded62becd45db27aa14`.
- After binary SHA256: `32dced33d62030383658c2209112136d5f55e81db00398d82c2e2c11e641604f`.
- Combined source manifest SHA256: `735e8989cf1ba30922fbdf82285872bcd1318f101d0099cb88b16c954c0e92b4`.
- Final manifests: `source-hashes.json`, `before-hashes.json`, `after-hashes.json`.
- Exact sequential delayed commands: `RUN_AFTER_CPU_QUIET.txt`; execute only after root global CPU-quiet GO. Then run the summary script on v7. v6 is superseded by v7 for final evidence.

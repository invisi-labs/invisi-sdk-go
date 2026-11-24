# Independent review of Go retained Shield preparation

The source comparison used the frozen nine-request library identified in `before-location.txt`. The review covered the production delta, the implementation report, the new regression tests, and the existing preparation, signing, submission, store and recovery paths. These findings describe the candidate as first reviewed. The implementer and coordinator own subsequent fixes, test execution and HTTP timings.

## P1 — Check the receipt after freezing the first submission

At `internal/transaction/broadcaster_submit.go:162` the managed first-send checks the retained receipt before writing its frozen packet. After both persistence callbacks, line 177 checks only the binding epoch. Replacement initialization, wallet checkpoint changes or a same-generation wallet reset during those callbacks can retire the receipt while preserving the epoch. Line 181 would then send under a retired receipt without full validation.

Check the scoped receipt immediately after the writes and before first transport. Exercise a store hook which retires the ticket during the final write and another which changes a checkpoint. Assert zero first transport calls and a durable, byte-identical packet that can subsequently be recovered.

## P1 — Authenticate the service on synchronization-based promotion

`session/shared_state.go:277` promotes the synchronization receipt without authenticating the current Protocol RPC service deployment. A reopened session can obtain its runtime at `internal/transaction/runtime.go:154` from persisted deployment evidence alone. A full state refresh can reuse available membership paths after confirming their roots on chain. With no pending reconcilable transaction, that refresh can complete without a single deployment read from the selected Protocol RPC service. Explicit initialization already authenticates this service at `session/shield_preparation_anchor.go:115`.

Use equivalent live service/deployment evidence for synchronization promotion. Reopen a wallet with a persisted snapshot and cached membership paths against a different service that fails or returns a mismatching deployment, then verify that full refresh cannot issue a retained receipt. Include the successful matching-service case.

## P1 — Preserve the previous private checked checkpoint

`session/shared_state.go:178` saves the prior wallet checkpoints only for an inventory-only refresh. For the full refresh, line 199 passes the wallet to a function that overwrites its checked checkpoint at line 133. The promotion at line 268 therefore checks the replacement head twice in the case where the prior checked checkpoint was the sole private marker. A changed or future prior checked block outside public coverage can escape validation entirely.

Preserve the original observed and checked blocks before mutating the wallet and authenticate them against the captured history before committing the updated markers. Test a native full refresh with valid token material, an orphaned prior checked block, and public coverage that does not include that block. Assert that the old private checkpoint survives the failure and that no receipt is published.

## P2 — Carry capture ownership through an active wallet scan

`session/shared_scan.go:206` retires the prior capture and discards its own ticket. At line 488 the same scan invokes public `RefreshState`, which starts another ticket at `session/shared_state.go:149`. An explicit initialization which starts later, while the scan is waiting for a decryptor or progress callback, can lose to this earlier scan when it resumes. This can replace a newly initialized token set or reverse a later failed replacement.

Carry the scan's original capture ownership through its final state refresh. Pause an active scan before that refresh, start a replacement, then finish the earlier scan. Verify ownership for both a successful replacement and a failed one.

## P2 — Give retained-mode journals a version older readers can reject

The retained operation adds an optional plan flag at `internal/transaction/preparation.go:82`, but `internal/transaction/complete.go:25` writes version 1 for all operations. The frozen SDK accepts version 1, ignores the additional JSON field, and interprets the new plan's `SubmissionValidation == "required"` as permission for legacy offline signing and export. An application rolling back its SDK can consequently resume a new ready operation and mark its call externalized without performing the intended live check. A write by that reader also drops the mode field.

Give retained plans a distinct wire version or another fail-closed discriminator understood by the frozen decoder. Verify new readers still resume old records, while an older reader rejects a newly retained record before any export or send. Whole-store tamper resistance is outside this compatibility finding.

## Verification limits

This was a source review with inspection of the test fixtures. No tests, benchmarks, builds, network transactions, or production-file edits were performed by the reviewer. In the ordinary retained path, nonce selection uses the actual payer at latest; the in-process plan clone carries the receipt; ordinary JSON decoding discards the private receipt; and frozen retries bypass first-send receipt validation. The coordinator's final verification should include the finding-specific regressions, compatibility suites and the common actual-HTTP driver.

## Independent re-review

| Initial finding | Disposition | Rechecked behavior |
| --- | --- | --- |
| Post-persistence first send | Addressed | `internal/transaction/broadcaster_submit.go:177` checks the binding epoch and retained wallet receipt after both journal writes and before transport. The check applies only to managed first sends. The uncertain path verifies frozen packet equality and supports original-configuration replay. A known request ID returns service status at line 155. |
| Sync service authentication | Addressed | `session/shared_state.go` calls the same live deployment validation as explicit initialization before committing material eligible for promotion. `session/shield_preparation_anchor.go:184` compares the selected service's deployment with the protocol. Failure and wrong deployment are covered on a reopened wallet. |
| Original private checkpoints | Addressed | The refresh saves both original markers at `session/shared_state.go:180`, verifies each full canonical block at line 227, and repeats the capture-head hash fence at line 250 after all historical requests. An orphan, future checkpoint, or branch change on the last missing height now rejects the refresh before commit. The wire regression exercises that last-read branch change and confirms the prior wallet marker survives. |
| Capture ownership during scan | Addressed | `session/shared_scan.go:191` claims a ticket before waiting for the active scan gate, and line 488 forwards that ticket to the final refresh. Staged refresh stores its original ticket and passes it through Decrypt at `session/pending_refresh.go:102`. Superseded scans cannot publish over a newer successful or failed capture. Both entry points and both replacement outcomes have regression coverage. |
| Versioned retained journal | Addressed | `internal/transaction/complete.go:27` writes version 2 for the retained mode. `internal/transaction/operation_recovery.go:15` rejects a missing mode bit on version 2, a retained bit on version 1, and inconsistent retained-mode metadata. Existing version-1 records continue to decode. The cross-version log records a current writer and the frozen reader rejecting version 2 and accepting its version-1 control. |

A JSON-restored version-2 operation has no private receipt. At first send `internal/transaction/broadcaster_submit.go:368` consequently rejects delegation and line 107 calls `ValidateExport`. Its required validation at `internal/transaction/operation_export.go:84` covers the historical anchor, current funding policy, balance and allowance, and the final canonical block. Manual export follows this validation. Pre-sign recovery follows the live authorization checks. The focused regressions exercise stale nonce before quote, after quote, before first submission, and before manual export.

The additional native recorder correction is addressed. At `internal/transaction/preparation_snapshot.go:162` the recorder validates its actual provider. On a network mismatch, `rpc/chain/protocol.go:170` invalidates the observed binding epoch by compare-and-swap. Other wallets using that epoch lose their old receipt and a previously retired response cannot invalidate the replacement epoch. The fixture covers both explicit initialization and full synchronization.

The reviewer inspected the code, regression assertions and recorded downgrade result. Full-module results and HTTP counts are verified separately by the coordinator.

### Reviewed source fingerprint

The worktree is based on Git commit `9182fc870449afddbf8fef5a110b1c40e059ffdd`. The following SHA-256 values identify the uncommitted production delta against the frozen source and the retained-mode regression file. The SHA-256 of the sorted manifest lines, including the final newline, is `750e49fedae929a9cb55890818e088e75ea0a4290e65053f14604f9b6aead691`.

```text
da3538f9178e1db0046150deef9529a829916aaa32402a3f2f5e01c1df4c5090  internal/transaction/broadcaster_submit.go
47753062b550a469e5527ad68ce56bb2ba531d838ae75b287cb4effe7daca06b  internal/transaction/complete.go
7541252783165c611576e60cf4719d585b816ee13e0780afffe9c63e2510bd5b  internal/transaction/execution.go
48797480b22f97c359b6eefaba8fada1077173468745d1b33435c859ea1ea209  internal/transaction/external_signing.go
d0dac0aa54358d4ec8e1c4ceaae51f2364b8d643d45fdef91e9805713a3a911c  internal/transaction/operation_recovery.go
03691865855922d88c569c0f6ed0a50d1899b22fb03ae5bbe0c1ea07dee22f9c  internal/transaction/preparation.go
5025a8dfd74c35a228b69c50ed609090faeca93131ce8547c97d9f5f03606d09  internal/transaction/preparation_snapshot.go
3a8d27510e9f33de153528bf0040a621aea49724a792585bb631f29a45c0ce42  internal/transaction/runtime.go
13b3436753cfbd3812167a935e3b5182963129e7022617f9950c53d9ca8f9d2f  rpc/chain/protocol.go
67ff444b394d710e62a73a72409ac14827cb24eecf0c8373b194135042b12b71  session/composition.go
3bf6445431a5d6f627c8bd0719032da6dbdfac8545c75f7ad9c7a90ce54e7da8  session/initialized_shield_test.go
2ffd4f6b3b1c5064c19d135543db3ac90140730bf59b669a2a3c73a482d78e6f  session/operation_export.go
5d8193146b798b02f655f6ae37b442a6f88f2687b976fdb42d664d8ae9f8c89a  session/pending_refresh.go
76a1e3d728e84395c278cf01b796c2a7ae452eaeb5433873347a34e7ac405f82  session/preparation.go
70714a87dcc52c68afeac2cee07ca9ae9f5e53bd9860cd75d5b7c421cf6b7239  session/reservations.go
c28de92eb3bdea9d84e482f3c8ea7f476dcfaee18b75ae92f0c1d9d6db2163f9  session/shared_scan.go
edb6d5a5d2e86026965a50a8bb98a72b0a30d8cca524f5bd9ab74858a10bcbd6  session/shared_state.go
f938368a0114d1dc83d99e19373399ec923554de7621a979645b0925d2c748e5  session/shield_preparation_anchor.go
```

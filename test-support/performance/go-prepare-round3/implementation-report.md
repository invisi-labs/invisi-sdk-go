# Go Shield preparation with a retained block

## API and adoption

Use an SDK-owned native HTTP binding for this managed wallet. After connecting, initialize the token once.

```go
if err := walletSession.InitializeShieldPreparation(ctx, token); err != nil {
	return err
}
op, err := walletSession.PrepareShield(ctx, sdk.ShieldPreparation{
	TokenAddress: token,
	Outputs:      outputs,
	Funding:      sdk.FundingSource{Payer: payer, Amount: amount},
})
```

Initialization authenticates the selected protocol service's deployment. A native node supplies the real header, block-pinned deployment checks, existing wallet checkpoint checks, and canonical token code. A node which explicitly rejects the hash selector incurs the height-based code read and final header check. An ambiguous selector error aborts initialization. Empty wallets require no synthetic notes, state imports, or extra checkpoints. Initialization is charged in the shared HTTP diagnostic. For an empty wallet with no private markers, a deployment without an address registry, and a node supporting EIP-1898, the new method itself makes six requests. These are one protocol-service deployment lookup, one header, one network identity, block-pinned core code and VERSION, and one token-code request with a canonical hash. If an address registry is configured, validation also checks its address. An older native deployment receipt does not identify the historical block, so initialization authenticates those pinned core facts. Prepare with an automatically chosen nonce adds two current requests on each eligible operation and no additional historical RPCs. An explicitly chosen nonce adds one current network request.

A successful full native state refresh can instead reuse its own block-pinned recorder. On a refresh that can promote token facts, the SDK also queries the selected protocol service and verifies its deployment before committing the wallet refresh. Reconciliation must complete, the current wallet metadata must match, the refresh must have recorded nonempty code for the token, and the checked and observed headers must match the final canonical block or recorded historical headers. Inventory-only refresh does not capture token material. The session neither serializes the receipt nor builds it by inspecting restored wallet JSON.

## Preparation and execution

The receipt records the concrete HTTP binding and epoch, protocol and service clients, wallet scope, private generation and reset revision, public generation, private observation/check markers, historical block, and verified token code hashes. Starting initialization or refresh retires the prior ticket. Publication uses compare-and-swap, so a delayed response cannot overwrite a replacement. A same-generation reset records the reset wallet revision. An existing operation may advance the wallet revision without losing the receipt.

An eligible signature-funded Shield with self-contained long-address note outputs prepares at the retained block. Its plan and outgoing history record that block. The local deadline uses the greater of the recorded timestamp and the current host time. The default deadline adds 300 seconds. The plan's public submission-validation flag is `required`. Automatic funding selection uses `ReadAuthorizationNonce` against the current network for each operation. It reads `eth_chainId` and the exact payer's `nonces(address)` at `latest`. A caller-provided nonce remains the caller's selection and the operation still checks the current network.

Preparation rechecks the receipt after nonce selection and before reservation. The wallet update callback checks cancellation, its private state and the binding ticket. An additional post-commit fence covers public history, which can use the same SQLite connection as the wallet. If that fence fails, cleanup conditionally removes only this worker's unexternalized prepared record. No Shield private inputs are reserved.

Retained online plans use journal plan version 2 and carry an explicit retained-mode flag. Current recovery checks both fields together. Version 1 keeps its existing semantics, and an older SDK rejects the new version before signing or export. Only the in-memory receipt enables local quoted signing and delegated simulation for this plan. A clone in the active flow preserves that receipt. JSON restoration loses it and reverts to current authorization and full first-send validation. Manual export retains live checks. Changed payer policy, a permit, schedule, opaque/registry recipients, private inputs, Credit and custom providers remain on their existing stage-specific paths. A frozen uncertain operation retries the exact packet against its original configuration, and a known request ID uses status only. A closed preparation binding does not block these two recovery routes.

## Regression evidence

The first actual-wire regression failed on the frozen library because the managed session lacked the initialization API. The synchronization test separately distinguished the prior snapshot preparation from the new native receipt. A third regression changed the private reset revision before first submission. The intermediate implementation sent using only two chain identity reads despite a stale selected nonce. The final managed path checks the operation's original process-local receipt and falls back to full validation when the wallet state changes.

The focused race suite passed with two actual payers and nonces 7 and 14, a separate explicit nonce, a changed public generation inside a wallet update, private checkpoints, epoch replacement during a nonce read and at reservation, cancellation, ambiguous and unsupported canonical selectors, missing token code, malformed nonce, wrong chain, failed refresh, late initialization, unknown-token fallback, scope exclusions, restart before quote/sign/send/export, and exact response-loss recovery after a closed binding.

```sh
go test -race ./session -run 'TestRetiredShieldAnchor|TestRetainedShieldJournal|TestSynchronizedShield|TestInitializedShield|TestFailedShieldInitialization|TestShieldInitialization|TestFailedNativeRefresh' -count=1
go test ./session -run 'TestRetiredShieldAnchor|TestRetainedShieldJournal|TestSynchronizedShield|TestInitializedShield|TestFailedShieldInitialization|TestLiveShieldCanonicalTokenFence|TestOwnedNativeManagedShield|TestStockBroadcasterDeny|TestCachedEOAAttach|TestFrozenJournalRestart' -count=1
```

Independent review identified two additional first-send and recovery boundaries. In a failing regression, retirement during the last journal write reached the first broadcaster transport once. The post-freeze fence now checks the full receipt as well as the binding epoch, while the durable packet remains recoverable. In a second failing regression, a reopened session's full refresh activated the fast path without querying its failing or mismatched protocol service. Refresh now authenticates that service before promotion. A third red test covered two wallets on one native binding. A wrong network observed by a recorder rejected that capture but left the other wallet's epoch active. The recorder now calls the same actual-provider network validation as the live route, in one wire read, and a wrong chain retires only the observed epoch. All of these cases passed in the combined focused race run.

```sh
go test -race ./rpc/chain ./session -run 'TestNativeCaptureWrongNetwork|TestRestoredWalletRefreshAuthenticatesActualProtocolService|TestRetiredShieldAnchor|TestRetainedShieldJournal|TestSynchronizedShield|TestInitializedShield|TestFailedShieldInitialization|TestShieldInitialization|TestFailedNativeRefresh|TestOwnedNativeManagedShield|TestStockBroadcasterDeny|TestOwnedHTTPBinding' -count=1
```

The remaining review regressions first reproduced loss of the original orphaned or future private checked checkpoint, replacement of later initialization by an older scan, and old-reader acceptance of the retained journal. Full native refresh now authenticates the original checked and observed blocks before committing replacement markers. An active scan and a staged refresh/decrypt carry their starting ticket into their final inventory refresh. A superseded scan can finish without installing its historical token set. Both successful and failed later initializations were exercised. The version-2 discriminator and mode-agreement tests cover restored journals. A cross-build overlay test wrote a real v2 record with the new SDK. The separately compiled frozen SDK rejected it and accepted a control identical except for version 1. The pass log is `downgrade-check.log`; temporary synthetic record contents were removed.

The expanded focused race command also included `TestNativeRefreshPreservesPriorPrivateCheckedCheckpoint`, `TestActiveWalletScanCannotReplaceLaterShieldInitialization`, `TestPendingRefresh`, and `TestOffline`. Both packages passed (rpc/chain 1.533s, session 20.505s).

A final review test switched the node to another branch while the recorder fetched a previously missing lower private checkpoint. Before the correction, the invocation committed a head from the first branch after accepting the lower block from the second. The existing final network and canonical head checks now run after every historical checkpoint read and the service query, immediately before the wallet commit. The correction adds zero requests. The final focused race rerun with this case passed in both packages (rpc/chain 1.862s, session 19.709s).

The shared diagnostic owns the comparative phase counts, charged first-use costs, and latency measurements. Final verification of the reviewed production source passed. `python3 scripts/test-modules.py` exited 0 after module-boundary checks and race tests, vet and build for nine modules. The full session race suite passed in 138.157s. `python3 scripts/check-packages.py` then exited 0 after packaging, independently extracting, testing, vetting and building all eight publishable modules through an isolated local proxy and cache. See `module-checks.log`, `archive-checks.log` and `downgrade-check.log` beside this report.

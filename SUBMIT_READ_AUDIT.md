# Managed broadcaster submit read audit (Go)

Audited base: `9182fc870449afddbf8fef5a110b1c40e059ffdd`.

## Change

A cached funding-signature first submit checked chain identity twice:
`ValidateExport -> Protocol.ValidateDeployment -> ChainID`, then
`validateCachedSubmission -> GethFundingVerifier -> VerifyTypedData -> ChainID`.

`ValidateExport` now passes the deployment chain identity already validated on
this invocation's provider to the default funding-signature verification helper.
Only the nil/default verifier path changes. Explicit/custom verifiers still run
unchanged, including an explicitly configured Geth verifier with another provider.
The public standalone verifier still reads and checks chain identity. ECDSA
verification and current code / ERC-1271 calls remain unchanged. No global cache,
SDK version change, dependency change, Credit change, or noncached signing change.

This saves one `eth_chainId` HTTP request per cached first validation. Chain
identity, deployment code/VERSION, deadlines, funding nonce/signature,
root/nullifier, preparation anchor, balance/allowance, and final canonical checks
remain. Known-ID retries query status; unknown-ID retries replay the frozen
request without rerunning first-submit checks.

## Actual localhost HTTP measurements

```
go test ./session -run TestSubmitActualHTTPAudit -v -count=1
```

Uses real go-ethereum `ethclient` HTTP against localhost JSON-RPC and real TLS HTTP
against a localhost broadcaster fixture. Public test key 1 signs Shield funding.
There are no inputs/roots/nullifiers, registry, provenance, or proof requests in
this fixture. Counts are wire HTTP requests, not ChainReader facade counters.
Chain fixture responses impose **10 ms per request**. Timing below is controlled
local latency, not real network or production performance.

| Preparation | Phase | Before chain HTTP | After chain HTTP | Relay HTTP |
| --- | --- | ---: | ---: | ---: |
| Noncached | funding export | 2 | 2 | 0 |
| Noncached | funding attach | 5 | 5 | 0 |
| Noncached | first submit | 5 | 5 | 2 |
| Cached | funding export | 0 | 0 | 0 |
| Cached | funding attach | 0 | 0 | 0 |
| Cached | first submit | 10 | 9 | 2 |
| Either | known-ID retry | 0 | 0 | 1 status |
| Either | unknown-ID retry | 0 | 0 | 1 frozen submit |

Cached and noncached first submit have different safety contracts: cached
preparation defers anchor, current funding authorization, balance, and allowance
checks until submit. Five noncached reads are **not** an equivalent target for
cached validation.

Cached first-submit methods change from `eth_chainId` 2 to 1, with
`eth_getCode` 1, `eth_getBlockByNumber` 3 (latest, anchor, final canonical), and
`eth_call` 4 (VERSION, nonce, balance, allowance) unchanged. Relay calls remain
`broadcaster_getStatus` 1 + `broadcaster_submitTransaction` 1.

An initial controlled run measured cached first submit at 128 ms before and
115 ms after; later runs under concurrent race/build load varied. Treat the
one-request reduction as the result, not a guaranteed wall-clock saving.

The request-count assertion was run before the production change and failed with
`first submit chain HTTP requests=10, want 9`, then passed after the change.

## Live read-only preflight

```
INVISI_SUBMIT_LIVE_INPUT=/absolute/path/native-network-input.json \
  go test ./session -run TestSubmitLiveReadHTTPAudit -v -count=1
```

Reads `options.chainId`, `options.chainRpcUrls[0]`, `options.protocolRpcUrl`, and
`tokenAddress`; never prints RPC endpoints. It discovers the real deployment,
uses public synthetic wallet key 1, and calls the live chain for deployment
code/VERSION/registry, headers, payer nonce, token balance, and allowance. **Only
balance and allowance response values are substituted locally**, after those real
reads, to let the synthetic wallet complete validation. No user keys, real funds,
proofs, relay requirements, or relay submissions are used. This is a live cached
Shield validation benchmark, not an end-to-end live submit.

The final transport before network IO permits only the needed read-only RPC
methods and rejects chain/relay sends. `TestLiveAuditTransportBlocksSubmissions`
checks that block. The live test never invokes a relay API.

A baseline-equivalent run explicitly selects the unchanged public Geth verifier
on the same provider, recreating the original default chain check. The optimized
run uses the default verifier. The configured BSC deployment has an address
registry check, adding one `eth_call` relative to localhost:

| Round | Baseline-equivalent HTTP | Optimized HTTP | Baseline ms | Optimized ms |
| --- | ---: | ---: | ---: | ---: |
| 1 | 11 | 10 | 2725 | 2801 |
| 2 | 11 | 10 | 2780 | 2797 |
| 3 | 11 | 10 | 2713 | 2057 |

Methods: `eth_chainId` 2 -> 1, `eth_getCode` 1, `eth_getBlockByNumber` 3,
`eth_call` 5. Network variance exceeded the saved round-trip in some runs;
there is no claim that every optimized request completes faster.

## Credit distinction

Credit has a separate state machine, without this ordinary funding-signature
path. Detached Credit checks current UTC usage period before and after quote /
requirements processing. Managed Credit validates submission, then checks the
period again before externalization. Cached managed Credit can also validate
preparation evidence before and after its live checks. These checks bracket slow
work and protect UTC period rollover / canonical evidence; do not remove them as
ordinary duplicate reads. Pure create/update/revoke have no frozen usage period.
Terminal Credit acknowledgements can require canonical reads before reservations
are released. No Credit behavior changes in this patch.

## Verification

New regression coverage verifies one deployment identity check, wrong-network
rejection before custom policy, custom verifier rejection, explicitly configured
Geth provider policy, standalone verifier network checks, and changing ERC-1271
signature policy / contract code across calls. Existing tests verify funding
nonce/balance/allowance changes, expired operations, spent inputs, invalid roots,
reorgs, lost responses, exact frozen retry, status-only recovery, and retained
reservations.

```
go test ./session -run 'TestCachedFunding|TestExplicitGethFunding|TestStandaloneFunding|TestLiveAuditTransport|TestSubmitActualHTTPAudit|TestOfflineShieldFunding|TestOfflineNoteSubmission|TestQuotedAggregateDefaults' -v -count=1
python3 scripts/test-modules.py
```

The repository script checks boundaries and runs race tests, vet, and build for
all nine modules. It completed successfully (exit 0), including full session
race tests (113.620 s). The latest targeted new regression tests also passed
under `-race`; `go vet ./session ./internal/transaction` and `git diff --check`
completed successfully.

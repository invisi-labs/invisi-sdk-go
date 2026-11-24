# Go live Shield preparation

## Outcome

The application can leave Funding.SigNonce unset to read the payer nonce inside preparation at the selected live block. This behavior exists on both source revisions. This change also combines token-code validation and final canonicality validation for a managed, signature-funded Shield built from self-contained long recipients and an SDK-owned HTTP binding. It sends one EIP-1898 eth_getCode request. Existing callers can continue to supply their own selected integer or use ReadAuthorizationNonce.

## Four reads in the original live frame

| Original request | Evidence used by preparation | Revised behavior |
| --- | --- | --- |
| eth_chainId | Validates this endpoint against the protocol's deployment. Any cached receipt covers immutable Core code, version and registry binding only. | Kept. The application can omit the separate identity read in ReadAuthorizationNonce when it selects the nonce through Funding instead. |
| eth_getBlockByNumber(latest, false) | Selects the height, hash, parent hash and chain timestamp. Used for expiry, default deadlines and the nonce lookup. | Kept for every live preparation. |
| eth_getCode(token, height) | Requires code at this token on the selected block. | Combined with the final canonicality fence on eligible owned HTTP preparations. |
| eth_getBlockByNumber(height, false) | Checks the selected hash and parent immediately before the operation enters the normal persistence path. | The combined call requires the very same block hash to be canonical at the time it reads the token code. A newer head can exist if that anchor remains canonical. |

The resulting successful preparation checks the exact token at the exact block hash selected by the live header. The hash commits to the header's height, parent and timestamp. Empty or malformed code, rejected or unknown anchor, transport error, cancellation, or retired binding fails before the new journal record is written. Wallet observation and checked-through anchors still take their own checks. The store still checks invalidation and reserves the actual operation inputs atomically. A Shield cannot reserve a private note through this path.

Token code and the live header cannot safely be initialized once for arbitrary EVM tokens. Code for an arbitrary token is a fact about a particular block. Reusing an initialization header would change the freshness contract and the operation's time basis. The revised live route retains these properties. The separate synchronized historical PreparationSnapshot flow keeps its own offline evidence and submission checks.

## Wire and API details

The shared diagnostic's adopted route calls PrepareShield with Funding.SigNonce set to nil. The normal generated Core caller sends eth_call with nonces(payer) and the selected numeric block. The caller integer, when provided, remains a selection without a claim of observed freshness. Native/live preparation returns its selected nonce in the same persisted operation and applies the existing quote, signing and submission policies.

HTTPBinding.CodeAtCanonicalHash(ctx, token, hash) returns code, supported, error. It sends eth_getCode with the token and an EIP-1898 selector carrying blockHash and requireCanonical set to true. Method-not-found (-32601) or an explicit selector-format rejection (-32602) opts into the older pair of numeric code and explicit canonical-header reads. The only recognized -32602 messages are exactly “unsupported block selector” and “invalid argument 1: json: cannot unmarshal object into Go value of type string”. Generic invalid-params, malformed or unknown hash, noncanonical and pruned-state replies fail closed. An unsupported reply is cached only as a capability fact on that particular binding epoch. Each following eligible preparation still performs both legacy state checks. The first unsupported request adds one probe, and independent concurrent first probes can overlap. Invalidate, closed bindings, and transport failures retain their existing lifetime and cancellation rules. Unknown hash (-32001), noncanonical (-32000), execution/read errors and malformed code fail closed. Successful code responses are always read again for a new preparation.

The combined route requires a live managed wallet, a concrete owned HTTPBinding, no custom funding verifier or permit, signature funding, and 1–10 complete long-address outputs. Explicit/manual preparation, other provider implementations, snapshots, caller-funded operations, opaque or registry recipients, transfers and Credit retain the existing paths. Later quote and submission options including scheduling, provenance and custom ERC-1271 policy still use their normal stage-specific validation and recovery.

## Verification

The initial live HTTP regression failed on the frozen behavior: successful preparation used two headers plus height-based token code. The replacement check was absent, and simulated canonical-token rejections did not protect the journal. A second test showed that unsupported selectors were reprobed across operations. A concurrent probe test then exposed an unnecessary invalidation of a replacement epoch on an old capability reply. Review then identified that some nodes attach -32602 to state errors. Five regressions showed the generic-code fallback would accept ambiguous invalid-params, unknown hash, noncanonical, pruned and malformed-hash responses. They now fail before journal creation. Each regression failed before its fix.

The focused race run passed across rpc/chain and session. The 27 session cases cover the wire selector and pinned nonce, separate wallet checkpoint checks and reorg detection, unsupported-node fallback and fallback reorg, noncanonical or unknown hash, missing and malformed code, transport/cancellation, wrong network, invalidation during and between reads, journal invalidation during preparation, and custom verifier/provider, permit, opaque and caller-funded fallback. Additional RPC tests cover negative capability lifetime, cancellation, re-probing after invalidation and an old response arriving after epoch replacement. The package's existing refusal and reconciliation suites run in the complete module checks.

Final-source verification passed. scripts/test-modules.py exited 0 after boundary checks and race tests, vet and build for nine modules, including examples. scripts/check-packages.py exited 0 after independently packaging, extracting, testing, vetting and building eight publishable modules. The respective logs are module-checks.log and archive-checks.log in this directory. The independently measured phase counts, timings and first-use costs belong to the shared diagnostic report.

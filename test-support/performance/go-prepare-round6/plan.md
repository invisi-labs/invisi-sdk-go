# Reuse preparation capability from initialized token-support response

User requests moving per-operation quotation capability discovery into initialization, preserving fee-lock security and recovery, then paired actual-HTTP count/timing verification. Base is already rebased develop a51f78dae98b11093f388e2e86c2df8b822cfc3e. No further rebase/commit/release/broadcast.

Primary local server evidence: invisi-broadcaster/internal/service/api.go GetTokenSupport initializes TokenSupport{ServiceStatus: a.GetStatus(), TokenAddress: token}. Use that actual response, validate full status and raw feeLock shape; do not add separate initialization getStatus or infer from options/JSON. Explicit native/cacheable InitializeBroadcasters can retain private copied capability under exact client/trust + discovery receipt/epoch/policy guard. Ordinary SupportsToken/unbound path remains fresh. Starting explicit initialization retires old claim; errors, cancellation, supersession, policy change and epoch change cannot resurrect it, including retained old client handles.

A successful unchanged live submission status preserves immutable capability. Failed/malformed/changed live status invalidates only the observed authority, with CAS ownership so an old request cannot retire a new initialization. Dynamic halt/provenance/scheduling checks stay live. Fee-lock-enabled operations still fetch individual signed tariff tickets and retain token/recipient/chain/protocol/signer/model/deadline/gas checks; invalid capability/ticket never silently downgrades. No generic cache for nonce, quotes or mutable service status.

1. Freeze baseline before production edits. Extend shared diagnostic to exercise both legacy and advertised fee-lock service; compile both versions with identical final driver.
2. Delegate scoped production/regression implementation rpc/broadcaster and rpc/chain using RED→GREEN; independent review after candidate. Root owns harness/docs/reports.
3. Default initialized ordinary flow8→7 with initialization unchanged6 (broadcast3 + Shield3). Advertised fee-lock flow9→8 due to retained lockFee. Uninitialized ordinary stays8. Include first/all-startup costs.
4. Full nine-module race/vet/build and eight extracted archive checks, then sequential20ms paired HTTP groups, three fixtures each two independent nonces/journals; verify actual requests, failures, source and binary hashes.
5. Report measured limits, safety/regression evidence, preserve original uncommitted work.

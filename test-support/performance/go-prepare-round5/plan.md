# Default native Protocol RPC transport

The user explicitly requests native transport by default when HTTPClient is absent, preserving custom clients and the existing safety/recovery boundaries, without rebasing develop.

The prior opt-in preserved the former global http.DefaultTransport behavior. New default: absent HTTPClient selects the same private native transport as explicit UseNativeHTTPTransport. Caller-supplied HTTPClient, including one with nil Transport, retains its policy and fresh service authentication. Keep the bool for compatibility, including rejection of true plus custom HTTPClient. No trust-policy or journal changes.

1. Freeze the exact round-four candidate before edits with unchanged shared diagnostic.
2. RED tests demonstrate no-options/zero-options use caller global policy and cannot reuse constructor verification; exact default prewarm observes4 instead of3. Test custom transport and custom nil transport separately.
3. Minimal constructor change; focused race including source cancellation/failure, restored state, capture supersession and deployment height checks.
4. Full nine-module race/vet/build and eight extracted archive checks; freeze final candidate.
5. Sequential paired native HTTP diagnostics at20ms: default before/after, custom before/after, explicit-native compatibility and default EIP1898 fallback. Independently verify counts and hashes, include cold/init costs, report limitations.

No commit, rebase, deploy or real transaction broadcast. Existing uncommitted work retained. Worktree remains inside project .worktree.

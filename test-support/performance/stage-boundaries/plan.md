# SDK stage boundary implementation plan

**Goal:** TS post-persistence send fencing first, then equivalent prepare/quote/ready/submit contracts across all three SDKs.
**Architecture:** immutable stage results carry business/funding/proof bindings forward; finite live checks protect first wire and durable frozen packets retain separate replay authority.
**Spec:** `spec.md` alongside this file, approved by the user's explicit implementation request.

## Global constraints

Use existing isolated optimization worktrees. Preserve all earlier uncommitted changes and HEADs; no rebase/commit/push/broadcast. Root owns unified drivers/reports. SDK owners own scoped production/regressions; review separately against frozen baselines, not Git HEAD. No IO under storage/cache locks. No default legacy downgrade, stale nonce reuse, partial ready publication or lost attempted packet.

## Review focus

- Provider/wallet change while awaited store persistence blocks must prevent first wire without losing retained recovery packet.
- Source proof failure/cancellation during ready must not publish a partial ready artifact or lose inputs.
- Fee-induced funding/change updates must preserve selected nonce, statement/output identities, authorization order, permit budget and original failure state.
- Restored missing local receipt/evidence must not silently expand submit into preparation/proving; attempted restore remains exact replay/status.
- Quote loops/scheduling/Credit/private change must not reenter full preparation or quietly relax validation.

### Task 1: TS final persistence fence
- [x] Reproduce blocked store/provider switch and failed no-wire control; implement final guard with recovery preservation.
- [x] Focused tests including normal pass, eventless/custom, disconnect, cancellation and original frozen replay.
- [x] Fresh scoped review before other refactor implementation.

### Task 2: TS stage refactor
- [x] Freeze funding prerequisites in prepare; replace quote general reprepare with bounded local repricing/explicit need-to-reprepare.
- [x] Finalize required Source proof in prove/ready, remove submit generation; stage regressions and documented errors.
- [x] Focused review then relevant package build/tests/typecheck/boundaries.

### Task 3: Go stage refactor
- [x] Keep selected funding prerequisites through fee adjustment; remove quote nonce selection/general preparation transitions.
- [x] Complete required Source proof before ready publication; submit missing-proof fails before wire, preserve replay.
- [x] Focused review then nine-module race/vet/build and relevant independent archives.

### Task 4: Rust stage refactor
- [x] Initialized prepare selects current nonce; quote consumes frozen funding terms and locally reprices.
- [x] Prove/ready finalizes required evidence before durable ready; first submit never builds missing Source evidence.
- [x] Focused review then workspace tests/checks with appropriate proof-resource limits.

### Task 5: Unified stages and final review
- [x] Root adds equivalent stage/HTTP diagnostics and private-Provenance/failure counters, freezes candidates and baseline drivers.
- [x] Run full suites without timing contention, then sequential actual-HTTP measurements with hashes and cold/setup costs.
- [x] Whole-contract review, close findings, final report distinguishes validated scope and exceptions.

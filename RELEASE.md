

## CI result reuse

Push checks run on `develop` and `main`; pull requests targeting either branch remain checked. `main` reuses successful `develop` push CI only for the identical commit SHA. Tag releases reuse successful `develop` or `main` push CI for that exact SHA; otherwise they run verification before release. Running, cancelled, skipped and failed results are never accepted. API failures block release instead of granting reuse. New merge commits require their own checks. Publishing remains tag-only, with package/archive validation retained. Branch pushes do not create releases.

Tagged releases validate the canonical version, every internal dependency pin, and all eight actual remote Git tags (including annotated tags) against the checked-out commit before checking archives. A missing dependency cache is treated as a cold start; external dependencies can be downloaded into the isolated cache.

Release jobs require `!cancelled()` as well as successful verification. Cancelling a workflow therefore also cancels publication work; completed registry writes remain immutable.

The current release cohort is **v1.3.5**: root SDK plus primitives, protocol, prover, rpc, session, ipp, and paymentrouter. Every internal module requirement uses the same version; optional modules publish only after their SDK and protocol dependencies.

## v1.3.5 publication

Run `python3 scripts/test-modules.py`, `python3 scripts/check-packages.py`, and
`python3 scripts/release-notes.py v1.3.5` before publication. Release notes are
rendered from the matching `CHANGELOG.md` section. Keep fee-lock model identifiers
and historical changelog sections at their original versions.

After the reviewed commit has reached `develop` and `main`, wait for successful
push CI at that exact commit. Create all eight tags on that same commit:

```sh
for module in primitives protocol prover rpc session ipp paymentrouter; do
  git tag "$module/v1.3.5" "$RELEASE_COMMIT"
done
git tag v1.3.5 "$RELEASE_COMMIT"
git push --atomic origin \
  primitives/v1.3.5 protocol/v1.3.5 prover/v1.3.5 rpc/v1.3.5 \
  session/v1.3.5 ipp/v1.3.5 paymentrouter/v1.3.5 v1.3.5
```

GitHub does not emit tag push events when more than three tags are pushed in
one operation. The eight-tag atomic publication above therefore requires the
explicit recovery/publication workflow, rather than waiting for a root-tag push
workflow that GitHub will not start:

```sh
gh workflow run recover-release.yml --ref main \
  -f release_tag=v1.3.5 -f expected_sha="$RELEASE_COMMIT"
```

The dispatcher runs from the selected trusted workflow revision, but checks out
the existing release tag and requires its actual commit to equal the explicit
full SHA. It independently requires successful develop/main push CI at that
exact release commit, verifies all eight actual remote tags, checks isolated
archives and renders the tagged changelog before publishing. The dispatcher
revision is never substituted for the release commit. Existing tags are not
rewritten or recreated. The ordinary single-root-tag workflow retains the same
validation for push events that GitHub does emit.

Wait for the dispatched release workflow to succeed, then resolve all eight
modules at `v1.3.5` from a fresh consumer without local replacement directives
before upgrading downstream applications.

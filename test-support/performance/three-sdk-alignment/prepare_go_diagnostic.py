#!/usr/bin/env python3
"""Prepare/compile source-hashed actual frozen-before and current-after diagnostics.
Does not run timed groups. Frozen inputs are copied, never edited in place.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
DIAGNOSTICS = ["three_sdk_http_diagnostic_test.go", "three_sdk_http_factory_before_test.go", "three_sdk_http_factory_after_test.go"]
MODULES = [".", "primitives", "protocol", "prover", "rpc", "session", "examples", "ipp", "paymentrouter"]

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def manifest(root):
    return {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob("*")) if p.is_file() and (p.suffix == ".go" or p.name in ("go.mod", "go.sum") or p.suffix == ".json") and ".git" not in p.relative_to(root).parts and ".worktree" not in p.relative_to(root).parts}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--frozen", type=Path, required=True)
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()
    out = args.out or Path(tempfile.mkdtemp(prefix="invisi-go-unified-http-"))
    out.mkdir(parents=True, exist_ok=True)
    before = out / "before"
    if before.exists():
        raise SystemExit(f"refusing to replace existing {before}")
    shutil.copytree(args.frozen, before)
    frozen_hashes = {str(p.relative_to(args.frozen)): digest(p) for p in sorted(args.frozen.rglob("*")) if p.is_file()}
    # Frozen snapshot deliberately stores text sources only. Bring unchanged
    # fixture/embedded JSON assets from the original working tree; hash them too.
    for p in ROOT.rglob("*.json"):
        relative = p.relative_to(ROOT)
        if any(part in (".git", ".worktree", "node_modules", "test-support") for part in relative.parts):
            continue
        dest = before / relative
        if not dest.exists():
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(p, dest)
    for name in DIAGNOSTICS:
        shutil.copy2(ROOT / "session" / name, before / "session" / name)
    # The public test helper implementations used by the harness already exist
    # in frozen source. The native factory is build-excluded from before.
    (before / "go.work").write_text("go 1.26.1\nuse (\n" + "".join(f" ./{m}\n" for m in MODULES) + ")\n")
    for relative, expected in frozen_hashes.items():
        if digest(before / relative) != expected:
            raise SystemExit(f"frozen source altered: {relative}")
    after = out / "after"
    after.mkdir()
    for relative in manifest(ROOT):
        src = ROOT / relative
        dest = after / relative
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src, dest)
    (after / "go.work").write_text((before / "go.work").read_text())
    details = {"beforeFrozenSourceHashes": frozen_hashes, "before": manifest(before), "after": manifest(after)}
    (out / "source-hashes.json").write_text(json.dumps(details, indent=2) + "\n")
    for variant, root, extra in [("before", before, ["-tags=three_sdk_before"]), ("after", after, [])]:
        hash_path = out / f"{variant}-hashes.json"
        subprocess.run(["go", "test", "-c", *extra, "./session", "-o", str(out / f"{variant}.test")], cwd=root, check=True)
        hash_path.write_text(json.dumps({"variant": variant, "sourceRoot": str(root), "sourceManifestPath": str(out / "source-hashes.json"), "sourceManifestSha256": digest(out / "source-hashes.json"), "binaryPath": str(out / f"{variant}.test"), "binarySha256": digest(out / f"{variant}.test"), "buildTags": extra, "files": details[variant]}, indent=2) + "\n")
    commands = []
    for variant in ("before", "after"):
        commands.append(f"INVISI_THREE_SDK_VARIANT={variant} INVISI_THREE_SDK_DELAY_MS=20 INVISI_THREE_SDK_HASHES='{out / (variant + '-hashes.json')}' INVISI_THREE_SDK_OUTPUT='{out / (variant + '.json')}' '{out / (variant + '.test')}' -test.run '^TestThreeSDKUnifiedActualHTTP$' -test.v")
    (out / "RUN_AFTER_CPU_QUIET.txt").write_text("\n\n".join(commands) + "\n")
    functional = [command.replace("INVISI_THREE_SDK_DELAY_MS=20", "INVISI_THREE_SDK_DELAY_MS=0").replace(f"/{variant}.json'", f"/{variant}-functional.json'") for variant, command in zip(("before", "after"), commands)]
    (out / "FUNCTIONAL_COMMANDS.txt").write_text("\n\n".join(functional) + "\n")
    print(json.dumps({"out": str(out), "frozenFilesVerified": len(frozen_hashes), "commands": commands}, indent=2))

if __name__ == "__main__":
    main()

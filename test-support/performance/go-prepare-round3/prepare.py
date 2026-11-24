#!/usr/bin/env python3
"""Freeze/compile a native candidate. Both variants use the same native factory."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

ROOT=Path(__file__).resolve().parents[3]
DRIVERS=['three_sdk_http_diagnostic_test.go','three_sdk_http_factory_after_test.go','three_sdk_http_factory_before_test.go']
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def files(root):
 return [p for p in sorted(root.rglob('*')) if p.is_file() and not any(x in p.relative_to(root).parts for x in ['.git','.worktree','test-support']) and (p.suffix in ['.go','.json'] or p.name in ['go.mod','go.sum','go.work'])]
p=argparse.ArgumentParser();p.add_argument('--source',type=Path,required=True);p.add_argument('--out',type=Path,required=True);p.add_argument('--variant',choices=['before','after'],required=True);a=p.parse_args()
a.out.mkdir(parents=True,exist_ok=True);dest=a.out/a.variant
if dest.exists():raise SystemExit(f'refusing to replace {dest}')
dest.mkdir()
for src in files(a.source):
 to=dest/src.relative_to(a.source);to.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(src,to)
for name in DRIVERS:shutil.copy2(ROOT/'session'/name,dest/'session'/name)
(dest/'go.work').write_text('go 1.26.1\nuse (\n'+''.join(f' ./{m}\n' for m in ['.','primitives','protocol','prover','rpc','session','examples','ipp','paymentrouter'])+')\n')
manifest={str(f.relative_to(dest)):sha(f) for f in files(dest)}
mp=a.out/f'{a.variant}-source-hashes.json';mp.write_text(json.dumps(manifest,indent=2)+'\n')
binary=a.out/f'{a.variant}.test'
subprocess.run(['go','test','-c','./session','-o',str(binary)],cwd=dest,check=True)
hp=a.out/f'{a.variant}-hashes.json';hp.write_text(json.dumps(dict(variant=a.variant,sourceRoot=str(dest),sourceManifestPath=str(mp),sourceManifestSha256=sha(mp),binaryPath=str(binary),binarySha256=sha(binary),buildTags=[],files=manifest),indent=2)+'\n')
command=f"INVISI_THREE_SDK_VARIANT={a.variant} INVISI_THREE_SDK_DELAY_MS=20 INVISI_THREE_SDK_HASHES='{hp}' INVISI_THREE_SDK_OUTPUT='{a.out/(a.variant+'.json')}' '{binary}' -test.run '^TestThreeSDKUnifiedActualHTTP$' -test.v"
(a.out/f'{a.variant}-command.txt').write_text(command+'\n')
print(command)

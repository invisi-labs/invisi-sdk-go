#!/usr/bin/env python3
"""Verify final native HTTP traces and keep API adoption/control groups separate."""
from pathlib import Path
from statistics import median
import hashlib,json,shutil
ROOT=Path(__file__).resolve().parents[3]
OUT=Path('/tmp/invisi-go-prepare-round2-v2-20261125')
REPORT=Path(__file__).resolve().parent
names=['before-explicit','before-merged','after-merged','after-legacy']
summary={}
def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def describe(xs):return dict(median=median(xs),min=min(xs),max=max(xs),values=xs)
for name in names:
 rawpath=OUT/f'{name}.json';raw=json.loads(rawpath.read_text());binary='before' if name.startswith('before') else 'after'
 manifest=json.loads((OUT/f'{binary}-hashes.json').read_text())
 assert sha(Path(manifest['binaryPath']))==manifest['binarySha256']
 assert sha(Path(manifest['sourceManifestPath']))==manifest['sourceManifestSha256']
 for p,h in manifest['files'].items():assert sha(Path(manifest['sourceRoot'])/p)==h,p
 assert len(raw['rows'])==42 and raw['artificialResponseDelayMs']==20
 assert raw['nonceMergedIntoPrepare']==(name!='before-explicit')
 assert raw['canonicalSelectorUnsupported']==(name=='after-legacy')
 for row in raw['rows']:
  assert row['ok'] and row['sourceHashes']==manifest
  assert len(row['attempts'])==row['actualHttpAttempts']
  for a in row['attempts']:
   assert a['method'].startswith(('eth_chainId','eth_call','eth_get','invisi_get','broadcaster_get','broadcaster_estimate'))
   if not a['ok']:assert name=='after-legacy' and row['transactionIndex']==1 and row['phase']=='prepare' and a['method']=='eth_getCode' and a['blockSelector']['requireCanonical']
 group=dict(inputSha256=sha(rawpath),sourceManifestSha256=manifest['sourceManifestSha256'],binarySha256=manifest['binarySha256'])
 for index,label in [(0,'coldSetup'),(0,'initialization'),(1,'first'),(2,'repeat')]:
  selected=[r for r in raw['rows'] if r['transactionIndex']==index and (index!=0 or r['phase']==label)]
  flows=[[r for r in selected if r['fixture']==f] for f in [1,2,3]]
  assert all(flows)
  counts=[sum(r['actualHttpAttempts'] for r in rows) for rows in flows]
  times=[sum(r['elapsedMs'] for r in rows) for rows in flows]
  denied=[sum(r['deniedWrites'] for r in rows) for rows in flows]
  assert denied==([0]*3 if index==0 else [1]*3)
  group[label]=dict(requests=counts,elapsedMs=describe(times),phases={r['phase']:r['actualHttpAttempts'] for r in flows[0]})
  if index:
   expected=11 if name=='before-explicit' or (name=='after-legacy' and index==1) else (9 if name=='after-merged' else 10)
   assert counts==[expected]*3
   for rows in flows:
    nonce=[a for r in rows for a in r['attempts'] if a['method']=='eth_call:7ecebe00']
    assert len(nonce)==1
    assert nonce[0]['blockSelector']==('latest' if name=='before-explicit' else '0xa')
    if name=='after-merged':
     code=[a for r in rows for a in r['attempts'] if a['method']=='eth_getCode'];assert len(code)==1 and code[0]['blockSelector']['requireCanonical'] is True
 group['firstIncludingStartup']=dict(requests=[group['coldSetup']['requests'][i]+group['initialization']['requests'][i]+group['first']['requests'][i] for i in range(3)],elapsedMs=describe([sum(group[x]['elapsedMs']['values'][i] for x in ['coldSetup','initialization','first']) for i in range(3)]))
 summary[name]=group
REPORT.mkdir(exist_ok=True);(REPORT/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
artifacts=REPORT/'results';artifacts.mkdir(exist_ok=True)
for name in names:
 shutil.copy2(OUT/f'{name}.json',artifacts/f'{name}.json')
 shutil.copy2(f'/tmp/invisi-go-prepare-{name}-timed.log',artifacts/f'{name}.log')
for binary in ['before','after']:
 for suffix in ['hashes.json','source-hashes.json']:
  shutil.copy2(OUT/f'{binary}-{suffix}',artifacts/f'{binary}-{suffix}')
for name,g in summary.items():print(name, 'first',g['first']['requests'],round(g['first']['elapsedMs']['median'],2),'repeat',g['repeat']['requests'],round(g['repeat']['elapsedMs']['median'],2))

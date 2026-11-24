#!/usr/bin/env python3
"""Independently verify counts and provenance, then summarize paired fixture costs."""
import hashlib
import json
from pathlib import Path
import shutil
from statistics import median
from run import CASES, OUT

ROOT = Path(__file__).resolve().parents[3]
REPORT = Path(__file__).resolve().parent
RESULTS = REPORT / 'results'
RESULTS.mkdir(exist_ok=True)

def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def describe(values):
    return dict(median=median(values), min=min(values), max=max(values), values=values)

summary = {}
baseline_scenario = None
for name, (binary, default, native, legacy) in CASES.items():
    rawpath = OUT / f'{name}.json'
    raw = json.loads(rawpath.read_text())
    manifest = json.loads((OUT / f'{binary}-hashes.json').read_text())
    assert sha(Path(manifest['binaryPath'])) == manifest['binarySha256']
    assert sha(Path(manifest['sourceManifestPath'])) == manifest['sourceManifestSha256']
    for path, digest in manifest['files'].items():
        assert sha(Path(manifest['sourceRoot']) / path) == digest, path
        if binary == 'after' and path != 'go.work':
            assert sha(ROOT / path) == digest, ('worktree changed', path)
    assert raw['sourceHashes'] == manifest
    if baseline_scenario is None:
        baseline_scenario = raw['scenario']
    assert raw['scenario'] == baseline_scenario
    before_sources = json.loads((OUT / 'before-source-hashes.json').read_text())
    for driver in ['three_sdk_http_diagnostic_test.go', 'three_sdk_http_factory_after_test.go',
                   'three_sdk_http_factory_before_test.go']:
        path = f'session/{driver}'
        assert manifest['files'][path] == before_sources[path], ('driver mismatch', path)
    assert len(raw['rows']) == 42 and raw['artificialResponseDelayMs'] == 20
    assert raw['nonceMergedIntoPrepare'] and raw['preparationAnchorRequested']
    assert raw['preparationAnchorAdoptions'] == [True] * 3
    assert raw['defaultServiceHTTP'] == bool(default)
    assert raw['nativeServiceRequested'] == bool(native)
    assert raw['nativeServiceAdoptions'] == [bool(native and binary == 'after')] * 3
    assert raw['canonicalSelectorUnsupported'] == bool(legacy)
    for row in raw['rows']:
        assert row['ok'] and row['sourceHashes'] == manifest
        assert len(row['attempts']) == row['actualHttpAttempts']
        for attempt in row['attempts']:
            if not attempt['ok']:
                assert legacy and row['phase'] == 'initialization'
                assert attempt['method'] == 'eth_getCode'
                assert attempt['blockSelector']['requireCanonical']
    group = dict(inputSha256=sha(rawpath),
                 sourceManifestSha256=manifest['sourceManifestSha256'],
                 binarySha256=manifest['binarySha256'])
    for index, label in [(0, 'coldSetup'), (0, 'initialization'), (1, 'first'), (2, 'repeat')]:
        selected = [r for r in raw['rows'] if r['transactionIndex'] == index
                    and (index != 0 or r['phase'] == label)]
        flows = [[r for r in selected if r['fixture'] == f] for f in [1, 2, 3]]
        assert all(flows)
        counts = [sum(r['actualHttpAttempts'] for r in rows) for rows in flows]
        times = [sum(r['elapsedMs'] for r in rows) for rows in flows]
        denied = [sum(r['deniedWrites'] for r in rows) for rows in flows]
        assert denied == ([0] * 3 if index == 0 else [1] * 3)
        group[label] = dict(requests=counts, elapsedMs=describe(times),
                            phases={r['phase']: r['actualHttpAttempts'] for r in flows[0]})
        if index:
            assert counts == [7] * 3
            for rows in flows:
                attempts = [a for r in rows for a in r['attempts']]
                nonce = [a for a in attempts if a['method'] == 'eth_call:7ecebe00']
                assert len(nonce) == 1 and nonce[0]['blockSelector'] == 'latest'
                assert sum(a['method'] == 'eth_chainId' for a in attempts) == 3
                assert not any(a['method'] in ['eth_getCode', 'eth_getBlockByNumber'] for a in attempts)
        elif label == 'coldSetup':
            assert counts == [7] * 3
        else:
            expected = 9 if binary == 'before' else 8 if legacy else 6 if native else 7
            assert counts == [expected] * 3, (name, counts)
            for rows in flows:
                attempts = rows[0]['attempts']
                methods = [a['method'] for a in attempts]
                assert methods.count('eth_call:ffa1ad74') == (1 if binary == 'before' else 0)
                assert methods.count('eth_getCode') == (2 if binary == 'before' or legacy else 1)
                assert methods.count('invisi_getDeployment') == (0 if native and binary == 'after' else 1)
                assert sum(not a['ok'] for a in attempts) == int(bool(legacy))
    for label, parts in [('firstIncludingStartup', ['coldSetup', 'initialization', 'first']),
                         ('twoIncludingStartup', ['coldSetup', 'initialization', 'first', 'repeat'])]:
        group[label] = dict(requests=[sum(group[p]['requests'][i] for p in parts) for i in range(3)],
                            elapsedMs=describe([sum(group[p]['elapsedMs']['values'][i] for p in parts) for i in range(3)]))
    group['prewarmAddedRequests'] = [n - 3 for n in group['initialization']['requests']]
    summary[name] = group
    shutil.copy2(rawpath, RESULTS / rawpath.name)
    shutil.copy2(f'/tmp/invisi-go-prewarm-{name}-timed.log', RESULTS / f'{name}.log')
for binary in ['before', 'after']:
    for suffix in ['hashes.json', 'source-hashes.json']:
        shutil.copy2(OUT / f'{binary}-{suffix}', RESULTS / f'{binary}-{suffix}')
(REPORT / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
for name, group in summary.items():
    print(name, {k: (v['requests'], round(v['elapsedMs']['median'], 2))
                 for k, v in group.items() if isinstance(v, dict) and 'requests' in v})

#!/usr/bin/env python3
"""Check measured stage contracts, exact recovery, and unchanged source snapshots."""
import hashlib
import json
from pathlib import Path
import sys
from summarize import private_rows

p = Path(sys.argv[1]).resolve()
checks = []


def check(condition, description):
    if not condition:
        raise AssertionError(description)
    checks.append(description)


def sha(file):
    return hashlib.sha256(file.read_bytes()).hexdigest()


for language in ['ts', 'go', 'rust']:
    for relative, digest in json.loads((p / f'{language}-before-manifest.json').read_text()).items():
        check(sha(p / f'{language}-before' / relative) == digest, f'{language} before source unchanged: {relative}')
    state = json.loads((p / 'final-source-state.json').read_text())[language]
    for relative, digest in json.loads((p / f'{language}-final-manifest.json').read_text()).items():
        check(sha(Path(state['root']) / relative) == digest, f'{language} candidate source unchanged: {relative}')

    driver_paths = {
        'ts': ['packages/session/src/operations/submit-http.profile.test.ts', 'packages/sdk/src/transactions/execution/PreparedProvenance.test.ts'],
        'go': ['session/three_sdk_http_diagnostic_test.go', 'session/stage_provenance_http_test.go', 'session/operation_test.go'],
        'rust': ['crates/invisi-sdk-session/tests/alignment_http.rs', 'crates/invisi-sdk-session/src/session/stage_source_boundary.rs', 'crates/invisi-sdk-session/src/session/source_reproof_tests.rs'],
    }
    baseline_driver_root = p / ('go-diagnostic/before' if language == 'go' else f'{language}-before-profile')
    for relative in driver_paths[language]:
        check(sha(baseline_driver_root / relative) == sha(Path(state['root']) / relative), f'{language}: identical paired driver {relative}')
    if language == 'rust':
        for variant in ['before', 'after']:
            build = json.loads((p / f'rust-isolated-{variant}-build.json').read_text())
            check(Path(build['targetRoot']).resolve() == p / f'rust-target-{variant}', f'Rust/{variant}: independent target directory')
            for scenario in ['ordinary', 'private']:
                check(sha(p / f'rust-{scenario}-{variant}.test') == build['artifacts'][scenario]['binarySha256'], f'Rust/{variant}/{scenario}: measured binary matches isolated build')
            artifacts = [json.loads(line) for line in (p / f'rust-isolated-ordinary-{variant}-artifacts.jsonl').read_text().splitlines()]
            local = [row for row in artifacts if row.get('reason') == 'compiler-artifact' and 'invisi' in row.get('package_id', '')]
            check(local and all(not row['fresh'] for row in local), f'Rust/{variant}: all SDK crates forcibly rebuilt')

    for variant in ['before', 'after']:
        files = list(p.glob(f'{language}-ordinary-{variant}-sample*.json'))
        check(len(files) == 1, f'{language}/{variant}: one three-fixture ordinary trace')
        data = json.loads(files[0].read_text())
        if language == 'ts':
            check(len(data['samples']) == 6, f'TS/{variant}: six ordinary operations')
            for sample in data['samples']:
                check(sample['deniedWrites'] == 1, f'TS/{variant}: innermost write denied')
                check(sample['fundingNonceUsed'] == str(7 * (sample['transactionIndex'] + 1)), f'TS/{variant}: selected nonce reaches final authorization')
                if variant == 'after':
                    check(sample['hotPath']['totalHttp'] == 8, 'TS/after: eight pre-broadcast HTTP reads')
                    check(not any(method.startswith('eth_') for method in sample['hotPath']['phases']['quote']['jsonRpcMethods']), 'TS/after: ordinary quote performs no chain RPC')
        elif language == 'go':
            totals = {}
            for row in data['rows']:
                if row['transactionIndex']:
                    key = (row['fixture'], row['transactionIndex'])
                    totals[key] = totals.get(key, 0) + row['actualHttpAttempts']
            check(len(totals) == 6, f'Go/{variant}: six ordinary operations')
            check(sum(row['deniedWrites'] for row in data['rows']) == 6, f'Go/{variant}: all ordinary writes denied')
            if variant == 'after':
                check(set(totals.values()) == {7}, 'Go/after: seven pre-broadcast HTTP reads')
                check(all(attempt['service'] == 'broadcaster' for row in data['rows'] if row['phase'] == 'quote' for attempt in row['attempts']), 'Go/after: ordinary quote performs no chain RPC')
        else:
            check(len(data) == 3, f'Rust/{variant}: three ordinary fixtures')
            for fixture in data:
                check(fixture['deniedWrites'] == 2 and fixture['actualWriteHttpAttempts'] == 0, f'Rust/{variant}: both ordinary writes denied')
                check(fixture['firstJournalByteIdenticalAfterSecond'], f'Rust/{variant}: first journal unchanged')
                if variant == 'after':
                    for index in [1, 2]:
                        check(sum(row['actualHttpAttempts'] for row in fixture['phases'] if row['transactionIndex'] == index) == 7, 'Rust/after: seven pre-broadcast HTTP reads')
                    check(all(row['actualHttpAttempts'] == 2 for row in fixture['phases'] if row['phase'] == 'quote'), 'Rust/after: ordinary quote has only two pricing requests')

        files = sorted(p.glob(f'{language}-private-{variant}-sample*.json'))
        check(len(files) == 3, f'{language}/{variant}: three private samples')
        for file in files:
            data = json.loads(file.read_text())
            rows = list(private_rows(language, data))
            by_stage = {row['stage']: row for row in rows}
            source_stage = 'ready' if variant == 'after' else ('firstSubmit' if language == 'rust' else 'submit')
            check(sum(row['mainProofs'] for row in rows) == 1, f'{file.name}: one main proof')
            check(sum(row['sourceProofs'] for row in rows) == 1, f'{file.name}: one Source proof')
            check(by_stage[source_stage]['sourceProofs'] == 1, f'{file.name}: Source belongs to {source_stage}')
            if variant == 'after':
                check(by_stage['quote']['chainFacadeCalls'] == 0, f'{file.name}: no quote chain facade reads')
                submit = by_stage['firstSubmit' if language == 'rust' else 'submit']
                check(submit['sourceProofs'] == submit['mainProofs'] == 0, f'{file.name}: submit never proves')
            if language == 'rust':
                check(len(data['relayRequests']) == 2 and data['relayRequests'][0] == data['relayRequests'][1], f'{file.name}: exact unknown-ID packet replay')
                check(by_stage['knownIdStatus']['relayFacadeCalls'] == 1, f'{file.name}: known-ID status only')
            else:
                raw_rows = data if isinstance(data, list) else data['rows']
                wires = [request for row in raw_rows for request in (row.get('requests') or []) if request['method'] == 'broadcaster_submitTransaction']
                check(len(wires) == 2 and wires[0]['params'] == wires[1]['params'], f'{file.name}: exact unknown-ID packet replay')
                status = [row for row in raw_rows if row['stage'] in ['status', 'knownIDStatus']][0]
                check(len(status['requests']) == 1 and status['requests'][0]['method'] == 'broadcaster_getTransactionStatus', f'{file.name}: known-ID status only')
                if variant == 'after':
                    quote = next(row for row in raw_rows if row['stage'] == 'quote')
                    check(not any(request['service'] in ['chain', 'protocol', 'source', 'Source'] for request in quote.get('requests') or []), f'{file.name}: quote only contacts pricing service')
            for row in rows:
                if row['stage'] in ['replay', 'unknownIdReplay', 'knownIDStatus', 'knownIdStatus', 'status', 'restore']:
                    check(row['chainFacadeCalls'] == row['mainProofs'] == row['sourceProofs'] == 0, f'{file.name}: {row["stage"]} does not read chain or prove')

result = dict(passed=len(checks), failed=0, checks=checks)
(p / 'verification.json').write_text(json.dumps(result, indent=2) + '\n')
print(f'{len(checks)} measured-stage/source/recovery checks passed')

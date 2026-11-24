#!/usr/bin/env python3
"""Run frozen paired diagnostics sequentially, after all builds/tests finish.

No external RPC endpoints are used. Ordinary drivers deny broadcaster writes;
private drivers use only fixture relays and counted synthetic proving backends.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--evidence', type=Path, required=True)
parser.add_argument('--ts-after', type=Path, required=True)
parser.add_argument('--delay-ms', type=int, default=20)
parser.add_argument('--samples', type=int, default=3)
parser.add_argument('--language', choices=['ts', 'go', 'rust'])
parser.add_argument('--scenario', choices=['ordinary', 'private'])
args = parser.parse_args()
root = args.evidence.resolve()
records_path = root / 'measurement-commands.json'
records = json.loads(records_path.read_text()) if records_path.exists() else []


def run(language, scenario, variant, sample):
    stem = f'{language}-{scenario}-{variant}-sample{sample}'
    output = root / (stem + '.json')
    env = dict(os.environ)
    cwd = root
    if language == 'ts':
        cwd = root / 'ts-before-profile' if variant == 'before' else args.ts_after
        command = ['pnpm', '--filter', '@invisi-labs/session' if scenario == 'ordinary' else '@invisi-labs/sdk',
                   'exec', 'vitest', 'run']
        if scenario == 'ordinary':
            # Existing driver groups three independent fixtures with two operations each.
            command += ['--config', '../../test-support/performance/vitest-submit-http.config.ts']
            report_dir = root / (stem + '-raw')
            env.update(SUBMIT_HTTP_PROFILE=str(report_dir), SUBMIT_HTTP_VARIANT=variant,
                       SUBMIT_HTTP_UNIFIED_LOCAL='1', SUBMIT_HTTP_EVENT_BRIDGE='1',
                       SUBMIT_HTTP_STAGE_CONTRACT='1', SUBMIT_HTTP_INITIALIZE_BROADCASTERS='1',
                       SUBMIT_HTTP_FUNDING_SNAPSHOT='1', SUBMIT_HTTP_TRANSACTIONS_PER_FIXTURE='2',
                       SUBMIT_HTTP_SAMPLES=str(args.samples), SUBMIT_HTTP_DELAY_MS=str(args.delay_ms),
                       SUBMIT_HTTP_CAPTURE_PARAMS='1')
            if variant == 'before':
                env['SUBMIT_HTTP_BASELINE_DIR'] = str(root / 'ts-before')
        else:
            command += ['src/transactions/execution/PreparedProvenance.test.ts', '-t', 'records private transfer stages']
            env.update(STAGE_BOUNDARY_PROFILE_OUTPUT=str(output), STAGE_BOUNDARY_DELAY_MS=str(args.delay_ms))
            if variant == 'before':
                env['STAGE_BOUNDARY_BASELINE'] = '1'
        command += ['--minWorkers=1', '--maxWorkers=1']
    elif language == 'go':
        command = [str(root / 'go-diagnostic' / (variant + '.test')), '-test.run',
                   '^TestThreeSDKUnifiedActualHTTP$' if scenario == 'ordinary' else '^TestStageProvenanceHTTP$', '-test.v']
        if scenario == 'ordinary':
            env.update(INVISI_THREE_SDK_VARIANT=variant, INVISI_THREE_SDK_DELAY_MS=str(args.delay_ms),
                       INVISI_THREE_SDK_HASHES=str(root / 'go-diagnostic' / (variant + '-hashes.json')),
                       INVISI_THREE_SDK_OUTPUT=str(output), INVISI_THREE_SDK_MERGE_NONCE='1',
                       INVISI_THREE_SDK_PREPARATION_ANCHOR='1', INVISI_THREE_SDK_DEFAULT_SERVICE='1',
                       INVISI_THREE_SDK_FEE_LOCK='0')
        else:
            env.update(STAGE_BOUNDARY_PROFILE_OUTPUT=str(output), STAGE_BOUNDARY_DELAY_MS=str(args.delay_ms))
    else:
        command = [str(root / f'rust-{scenario}-{variant}.test')]
        if scenario == 'ordinary':
            command += ['--ignored', '--exact', 'ordinary_shield_http_alignment', '--nocapture']
            env.update(INVISI_ALIGNMENT_STAGE_CONTRACT='1', INVISI_ALIGNMENT_DELAY_MS=str(args.delay_ms),
                       INVISI_ALIGNMENT_FIXTURES=str(args.samples), INVISI_ALIGNMENT_OUTPUT=str(output))
        else:
            command += ['private_source_stage_boundaries_and_exact_recovery', '--nocapture']
            env.update(INVISI_STAGE_NO_READY_FAILURE='1', INVISI_ALIGNMENT_DELAY_MS=str(args.delay_ms),
                       INVISI_STAGE_SOURCE_TRACE=str(output))
            if variant == 'before':
                env['INVISI_STAGE_BASELINE'] = '1'
    started = time.time()
    print(stem, flush=True)
    with (root / (stem + '.log')).open('w') as log:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=log, stderr=subprocess.STDOUT)
    records.append(dict(name=stem, command=command, cwd=str(cwd),
                        environment={key: value for key, value in env.items() if key in {
                            'SUBMIT_HTTP_PROFILE', 'SUBMIT_HTTP_VARIANT', 'SUBMIT_HTTP_UNIFIED_LOCAL',
                            'SUBMIT_HTTP_EVENT_BRIDGE', 'SUBMIT_HTTP_STAGE_CONTRACT', 'SUBMIT_HTTP_INITIALIZE_BROADCASTERS',
                            'SUBMIT_HTTP_FUNDING_SNAPSHOT', 'SUBMIT_HTTP_TRANSACTIONS_PER_FIXTURE', 'SUBMIT_HTTP_SAMPLES',
                            'SUBMIT_HTTP_DELAY_MS', 'SUBMIT_HTTP_CAPTURE_PARAMS', 'SUBMIT_HTTP_BASELINE_DIR',
                            'STAGE_BOUNDARY_PROFILE_OUTPUT', 'STAGE_BOUNDARY_DELAY_MS', 'STAGE_BOUNDARY_BASELINE',
                            'INVISI_THREE_SDK_VARIANT', 'INVISI_THREE_SDK_DELAY_MS', 'INVISI_THREE_SDK_HASHES',
                            'INVISI_THREE_SDK_OUTPUT', 'INVISI_THREE_SDK_MERGE_NONCE', 'INVISI_THREE_SDK_PREPARATION_ANCHOR',
                            'INVISI_THREE_SDK_DEFAULT_SERVICE', 'INVISI_THREE_SDK_FEE_LOCK',
                            'INVISI_ALIGNMENT_STAGE_CONTRACT', 'INVISI_ALIGNMENT_DELAY_MS', 'INVISI_ALIGNMENT_FIXTURES',
                            'INVISI_ALIGNMENT_OUTPUT', 'INVISI_STAGE_NO_READY_FAILURE', 'INVISI_STAGE_SOURCE_TRACE', 'INVISI_STAGE_BASELINE'}},
                        startedAt=started, elapsedSeconds=time.time() - started, returncode=result.returncode))
    records_path.write_text(json.dumps(records, indent=2) + '\n')
    if result.returncode:
        raise SystemExit(f'{stem} failed; see its log')
    if language == 'ts' and scenario == 'ordinary':
        output.write_bytes((report_dir / f'local-{variant}.json').read_bytes())
    if not output.is_file():
        raise SystemExit(f'{stem} produced no trace')


for language in [args.language] if args.language else ['ts', 'go', 'rust']:
    for scenario in [args.scenario] if args.scenario else ['ordinary', 'private']:
        for sample in range(1, 2 if scenario == 'ordinary' else args.samples + 1):
            for variant in ['before', 'after']:
                run(language, scenario, variant, sample)

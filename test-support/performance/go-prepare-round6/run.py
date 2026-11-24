#!/usr/bin/env python3
"""Run final frozen candidates sequentially; never broadcasts outside the fixture."""
import argparse
import os
from pathlib import Path
import subprocess

OUT = Path('/tmp/invisi-go-prepare-round6-v2-20261125')
CASES = {
    'before-legacy': ('before', 0),
    'after-legacy': ('after', 0),
    'before-fee-lock': ('before', 1),
    'after-fee-lock': ('after', 1),
}

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('case', choices=CASES)
    parser.add_argument('--delay', type=int, default=20)
    args = parser.parse_args()
    binary, fee_lock = CASES[args.case]
    env = os.environ.copy()
    values = dict(VARIANT=args.case, DELAY_MS=args.delay,
                  PREPARATION_ANCHOR=1, MERGE_NONCE=1,
                  DEFAULT_SERVICE=1, NATIVE_SERVICE=0,
                  CANONICAL_UNSUPPORTED=0, FEE_LOCK=fee_lock,
                  HASHES=OUT / f'{binary}-hashes.json',
                  OUTPUT=OUT / f'{args.case}.json')
    env.update({f'INVISI_THREE_SDK_{k}': str(v) for k, v in values.items()})
    log = Path(f'/tmp/invisi-go-capability-{args.case}-timed.log')
    with log.open('w') as stream:
        subprocess.run([str(OUT / f'{binary}.test'), '-test.run',
                        '^TestThreeSDKUnifiedActualHTTP$', '-test.v'],
                       env=env, stdout=stream, stderr=subprocess.STDOUT, check=True)
    print(f'{args.case}: PASS, {log}')

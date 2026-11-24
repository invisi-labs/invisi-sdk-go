#!/usr/bin/env python3
"""Run final frozen candidates sequentially; never broadcasts outside the fixture."""
import argparse
import os
from pathlib import Path
import subprocess

OUT = Path('/tmp/invisi-go-prepare-round5-20261125')
CASES = {
    'before-default': ('before', 1, 0, 0),
    'after-default': ('after', 1, 0, 0),
    'before-custom': ('before', 0, 0, 0),
    'after-custom': ('after', 0, 0, 0),
    'after-explicit-native': ('after', 1, 1, 0),
    'after-default-legacy': ('after', 1, 0, 1),
}

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('case', choices=CASES)
    parser.add_argument('--delay', type=int, default=20)
    args = parser.parse_args()
    binary, default, native, legacy = CASES[args.case]
    env = os.environ.copy()
    values = dict(VARIANT=args.case, DELAY_MS=args.delay,
                  PREPARATION_ANCHOR=1, MERGE_NONCE=1,
                  DEFAULT_SERVICE=default, NATIVE_SERVICE=native,
                  CANONICAL_UNSUPPORTED=legacy,
                  HASHES=OUT / f'{binary}-hashes.json',
                  OUTPUT=OUT / f'{args.case}.json')
    env.update({f'INVISI_THREE_SDK_{k}': str(v) for k, v in values.items()})
    log = Path(f'/tmp/invisi-go-default-{args.case}-timed.log')
    with log.open('w') as stream:
        subprocess.run([str(OUT / f'{binary}.test'), '-test.run',
                        '^TestThreeSDKUnifiedActualHTTP$', '-test.v'],
                       env=env, stdout=stream, stderr=subprocess.STDOUT, check=True)
    print(f'{args.case}: PASS, {log}')

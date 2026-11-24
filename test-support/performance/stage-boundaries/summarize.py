#!/usr/bin/env python3
"""Normalize paired stage traces without conflating facade calls and HTTP.

Usage: summarize.py /absolute/evidence-directory
Raw request params remain in the input JSON; this emits compact stage medians.
"""
import json
from pathlib import Path
from statistics import median
import sys


def private_rows(language, data):
    if language == 'rust':
        previous = {}
        for row in data['stages']:
            def delta(key):
                return row.get(key, 0) - previous.get(key, 0)
            yield dict(stage=row['stage'], elapsedMs=delta('elapsedMicrosSinceStart') / 1000,
                       http=delta('sourceHttp'), mainProofs=delta('mainProver'),
                       sourceProofs=delta('sourceProver'),
                       chainFacadeCalls=sum(delta(k) for k in ['chainNonce', 'chainNullifier', 'chainRoot', 'chainSnapshot']),
                       relayFacadeCalls=delta('relayWire') + delta('status'))
            previous = row
    else:
        for row in data if isinstance(data, list) else data['rows']:
            yield dict(stage=row['stage'], elapsedMs=row['elapsedMs'],
                       http=len(row.get('requests') or []),
                       mainProofs=row.get('mainProofs', row.get('mainProves', 0)),
                       sourceProofs=row.get('sourceProofs', row.get('sourceProves', 0)),
                       chainFacadeCalls=len(row.get('readerCalls') or []),
                       protocolFacadeCalls=len(row.get('protocolCalls') or []))


def ordinary_rows(language, data):
    if language == 'go':
        for row in data['rows']:
            if row['method'] == 'phase-total':
                yield dict(stage=row['phase'], transactionIndex=row['transactionIndex'],
                           elapsedMs=row['elapsedMs'], http=row['actualHttpAttempts'])
    elif language == 'rust':
        for fixture in data:
            for row in fixture['phases']:
                yield dict(stage=row['phase'], transactionIndex=row['transactionIndex'],
                           elapsedMs=row['elapsedMs'], http=row['actualHttpAttempts'])
    else:
        for sample in data['samples']:
            for name, row in sample['hotPath']['phases'].items():
                yield dict(stage=name, transactionIndex=sample['transactionIndex'] + 1,
                           elapsedMs=row['elapsedMs'], http=row['httpAttempts'])
            if sample['transactionIndex'] == 0:
                for name in ['coldSetup', 'oneTimeInitialization']:
                    row = sample[name]
                    yield dict(stage=name, transactionIndex=0,
                               elapsedMs=row['elapsedMs'], http=row['httpAttempts'])


def summarize(rows):
    groups = {}
    for row in rows:
        groups.setdefault((row['stage'], row.get('transactionIndex')), []).append(row)
    result = []
    for (stage, index), group in groups.items():
        item = dict(stage=stage, samples=len(group))
        if index is not None:
            item['transactionIndex'] = index
        for field in group[0]:
            if field not in ['stage', 'transactionIndex']:
                values = [row[field] for row in group]
                item[field] = round(median(values), 3)
                if field != 'elapsedMs' and len(set(values)) != 1:
                    item[field + 'Range'] = [min(values), max(values)]
        result.append(item)
    return result


def main(directory):
    directory = Path(directory)
    report = {}
    for language in ['ts', 'go', 'rust']:
        report[language] = {}
        for scenario, normalize in [('ordinary', ordinary_rows), ('private', private_rows)]:
            report[language][scenario] = {}
            for variant in ['before', 'after']:
                files = sorted(directory.glob(f'{language}-{scenario}-{variant}-sample*.json'))
                rows = []
                for file in files:
                    rows.extend(normalize(language, json.loads(file.read_text())))
                report[language][scenario][variant] = summarize(rows)
    target = directory / 'summary.json'
    target.write_text(json.dumps(report, indent=2) + '\n')
    print(target)


if __name__ == '__main__':
    main(sys.argv[1])

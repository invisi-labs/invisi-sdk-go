#!/usr/bin/env python3
"""Summarize comparable before/after JSON; preserve first/repeat groups."""
import argparse
from collections import Counter
import json
from pathlib import Path
from statistics import mean, median

def describe(values):
    return {"samples":len(values),"min":min(values),"median":median(values),"mean":mean(values),"max":max(values)}

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("directory",type=Path)
    a=p.parse_args()
    result={"language":"go","groups":{},"notes":["Source hashes are in source-hashes.json and per-variant hash manifests.","Each transaction sums every phase from nonce prerequisite through innermost denied broadcast.","Cold setup and initialization are separate actual measurements, charged once per fixture.","Compilation, executable pre-main loader delay and test framework startup are outside phase timers.","Before executes unmodified frozen production; only diagnostic tests and identical embedded fixture assets were added.","Before has no explicit nonce-only API: its existing preparation reads the nonce. After selects a fresh nonce and uses it in preparation.","Both variants use the identical test-only deny RoundTripper. Concrete stock clients retain quote, intent and service binding checks; custom chain providers and higher-level verifier policies remain conservative.","Actual HTTP attempts count at loopback service handlers; denied writes never reach HTTP.","Normal operation uses its in-memory native local quote receipt. JSON restore loses that unsigned local receipt and uses conservative revalidation; separate recovery regressions cover restore."]}
    for variant in ("before","after"):
        raw=json.loads((a.directory/f"{variant}.json").read_text())
        rows=raw["rows"]
        group={"artificialResponseDelayMs":raw["artificialResponseDelayMs"],"coldSetup":{},"initialization":{},"first":{},"repeat":{}}
        for index,label in [(0,"coldSetup"),(0,"initialization"),(1,"first"),(2,"repeat")]:
            selected=[r for r in rows if r["transactionIndex"]==index and (index!=0 or r["phase"]==label)]
            elapsed=[];counts=[];methods=Counter();denied=0;phase_totals={}
            for fixture in (1,2,3):
                fixture_rows=[r for r in selected if r["fixture"]==fixture]
                if not fixture_rows or any(not r["ok"] for r in fixture_rows):raise ValueError(f"missing/failed {variant}/{fixture}/{label}")
                elapsed.append(sum(r["elapsedMs"] for r in fixture_rows));counts.append(sum(r["actualHttpAttempts"] for r in fixture_rows))
            for r in selected:
                denied+=r["deniedWrites"]
                for attempt in r["attempts"]:methods[f'{attempt["service"]}/{attempt["method"]}']+=1
                phase_totals.setdefault(r["phase"],[]).append(r["elapsedMs"])
            group[label]={"elapsedMs":describe(elapsed),"actualHttpAttempts":counts,"deniedWrites":denied,"httpMethodsAcrossThreeFixtures":dict(sorted(methods.items())),"phasesElapsedMs":{k:describe(v) for k,v in phase_totals.items()}}
        result["groups"][variant]=group
    (a.directory/"summary.json").write_text(json.dumps(result,indent=2)+"\n")
    lines=["# Go unified actual HTTP diagnostic", "", "Actual frozen-before production versus aligned-after production. Three fixtures each, two signature-funded Shield flows per fixture with reused initialized objects and intact first journal.","", "| Variant | Group | HTTP per fixture | Median ms | Min–max ms | Denied writes |", "|---|---|---|---:|---:|---:|"]
    for variant,g in result["groups"].items():
        for label in ("coldSetup","initialization","first","repeat"):
            v=g[label];e=v["elapsedMs"]
            lines.append(f'| {variant} | {label} | {v["actualHttpAttempts"]} | {e["median"]:.2f} | {e["min"]:.2f}–{e["max"]:.2f} | {v["deniedWrites"]} |')
    lines += ["", "Business amount 30 + signed fee 5 = signature funding 35; fee cap 10. One embedded long-address output, no proof/private inputs/provenance/permit/schedule. Public fixture keys 01/02 only.", ""]
    lines += ["- "+n for n in result["notes"]]
    (a.directory/"REPORT.md").write_text("\n".join(lines)+"\n")
    print("\n".join(lines))

if __name__=="__main__":main()

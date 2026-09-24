#!/usr/bin/env python3
"""Separate nested CPU work from serialized GPU work in a completed replay."""
import argparse
import json
import math
from pathlib import Path
import statistics


def analyze(run):
    meta = json.loads((run / 'run.json').read_text())
    summary = json.loads((run / 'summary.json').read_text())
    if not summary['complete']:
        raise ValueError('completed verified run required')
    rows = [json.loads(s) for s in (run / 'metrics.jsonl').read_text().splitlines()]
    proofs = [r for r in rows if r['event'] == 'proof' and not r['context']['warmup'] and r['verified']]
    if len(proofs) != summary['expected_measured']:
        raise ValueError('proof count mismatch')
    samples = []
    for proof in proofs:
        seq = proof['context']['sequence']
        selected = [r for r in rows if r['context'].get('sequence') == seq]
        durations = {}
        for name in ['solver', 'prover_compute', 'compute_h', 'groth16_prove']:
            matched = [r for r in selected if r['event'] in ['stage', 'internal_stage'] and r.get('stage') == name]
            if len(matched) != 1:
                raise ValueError('missing or duplicate stage: ' + name)
            durations[name + '_ms'] = matched[0]['duration_ms']
        details = [r for r in selected if r['event'] == 'msm_detail']
        if len(details) != 5 or {r['stage'] for r in details} != {'msm_g1_A', 'msm_g1_B', 'msm_g1_K', 'msm_g1_Z', 'msm_g2_B2'}:
            raise ValueError('expected exactly five MSM details')
        for r in details:
            if (not r['ok'] or r['backend_ms'] < 0 or r['wait_ms'] < 0 or r['exclusive_ms'] < r['backend_ms']
                    or r['chunks'] != math.ceil(r['points'] / meta['config']['msm_chunk_size'])):
                raise ValueError('invalid MSM timing record')
        durations.update(msm_exclusive_sum_ms=sum(r['exclusive_ms'] for r in details),
                         msm_backend_sum_ms=sum(r['backend_ms'] for r in details),
                         msm_queue_sum_ms=sum(r['wait_ms'] for r in details))
        durations['compute_h_share_percent'] = 100 * durations['compute_h_ms'] / durations['groth16_prove_ms']
        durations['msm_share_percent'] = 100 * durations['msm_exclusive_sum_ms'] / durations['groth16_prove_ms']
        samples.append({'scenario': proof['context']['scenario'], 'sequence': seq, **durations})
    scenarios = {}
    for name in sorted({r['scenario'] for r in samples}):
        subset = [r for r in samples if r['scenario'] == name]
        scenarios[name] = {'n': len(subset), **{key: statistics.median(r[key] for r in subset)
                                              for key in subset[0] if key not in ['scenario', 'sequence']}}
    return {'complete': True, 'profile_enabled': meta['config']['profile'], 'scenarios': scenarios,
            'samples': samples,
            'notes': ['compute_h and exclusive MSM work are nested in prover_compute, itself nested in groth16_prove.',
                      'Exclusive MSM intervals are serialized by the engine mutex; their sum excludes queue waits.',
                      'backend_ms includes copies, conversion and synchronous CUDA calls; it is not kernel-only time.',
                      'Sum of concurrent queue waits is diagnostic, not a critical-path duration.',
                      'Profiling and instrumentation can perturb timing; this is bottleneck localization, not a new speed baseline.']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('run', type=Path)
    args = parser.parse_args()
    print(json.dumps(analyze(args.run), indent=2))

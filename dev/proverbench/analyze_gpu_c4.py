#!/usr/bin/env python3
"""Aggregate ABBA runs; do not compare mismatched builds or failed proofs."""
import json
from pathlib import Path
import statistics

HERE = Path(__file__).resolve().parents[2] / 'lab-private/gpu-c4-paired-analysis-20260924'
HERE.mkdir(parents=True, exist_ok=True)
ROOT = HERE.parent
ORDER = ['a1', 'b1', 'b2', 'a2']


def read_jsonl(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def describe(values):
    return dict(n=len(values), median=statistics.median(values),
                minimum=min(values), maximum=max(values))


def background_cpu(rows):
    valid = [r for r in rows if 'host_cpu_busy_s' in r]
    a, b = valid[0], valid[-1]
    elapsed = b['monotonic_s'] - a['monotonic_s']
    host = b['host_cpu_busy_s'] - a['host_cpu_busy_s']
    own = b['user_cpu_s'] + b['system_cpu_s'] - a['user_cpu_s'] - a['system_cpu_s']
    return max(0, host - own) / elapsed


runs = {}
baseline = None
pool = {'a': {}, 'b': {}}
stage_pool = {'a': {}, 'b': {}}
for name in ORDER:
    p = ROOT / ('gpu-c4-paired-' + name + '-20260924')
    meta = json.loads((p / 'run.json').read_text())
    summary = json.loads((p / 'summary.json').read_text())
    assert summary['complete'], name + ' incomplete'
    assert summary['verified_measured'] == 9 and summary['verified_warmup'] == 9
    invariant = {k: meta[k] for k in ['build', 'samples', 'input_sha256', 'host', 'kernel', 'cpu_model', 'affinity', 'gpu']}
    # GPU metadata includes dynamic memory/utilization at launch; compare identity only.
    invariant['gpu'] = {k: v for k, v in meta['gpu'].items() if k not in ['memory.used', 'utilization.gpu']}
    invariant['config'] = {k: v for k, v in meta['config'].items() if k not in ['msm_chunk_size', 'msm_internal_chunks']}
    if baseline is None:
        baseline = invariant
    assert invariant == baseline, name + ' differs beyond chunk configuration'
    expected = (262144, 1) if name.startswith('a') else (1048576, 4)
    assert (meta['config']['msm_chunk_size'], meta['config']['msm_internal_chunks']) == expected
    metrics = read_jsonl(p / 'metrics.jsonl')
    measured = [r for r in metrics if r['event'] == 'stage' and r['stage'] == 'groth16_prove'
                and r['context'].get('warmup') is False and r['ok']]
    assert len(measured) == 9
    ext = read_jsonl(p / 'external.jsonl')
    per_scenario = {}
    for scenario in ['bridge', 'deposit', 'withdrawal']:
        selected = [r['duration_ms'] for r in measured if r['context']['scenario'] == scenario]
        per_scenario[scenario] = describe(selected)
        pool[name[0]].setdefault(scenario, []).extend(selected)
        for stage in ['solver', 'prover_compute']:
            times = [r['duration_ms'] for r in metrics if r['event'] == 'internal_stage'
                     and r['stage'] == stage and r['context'].get('warmup') is False
                     and r['context']['scenario'] == scenario and r.get('ok', True)]
            assert len(times) == 3
            stage_pool[name[0]].setdefault(scenario + '/' + stage, []).extend(times)
    runs[name] = dict(scenarios=per_scenario, gpu=summary['gpu'], rss_hwm_bytes=summary['rss_hwm_bytes'],
                      swap_max_bytes=max(r.get('swap_bytes', 0) for r in ext),
                      estimated_background_cpu_cores=background_cpu(ext))

comparisons = {}
for scenario in ['bridge', 'deposit', 'withdrawal']:
    a, b = describe(pool['a'][scenario]), describe(pool['b'][scenario])
    comparisons[scenario] = dict(old=a, candidate=b, reduction_percent=100 * (1 - b['median'] / a['median']))

result = dict(order=ORDER, runs=runs, comparisons=comparisons, verified_proofs=72,
              stage_medians={label: {key: statistics.median(values) for key, values in stages.items()}
                             for label, stages in stage_pool.items()},
              binary_sha256=baseline['build']['binary_sha256'],
              notes=['Nine warmups and nine measured proofs per run; two runs per configuration.',
                     'Only Go groth16_prove; not full RPC/FFI/plonky2 latency.',
                     'Both outer window and internal chunks change together; gains cannot be assigned to chunks alone.',
                     'GPU peaks are sampled; small samples and shared host limit generalization.'])
(HERE / 'comparison.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(result, indent=2))

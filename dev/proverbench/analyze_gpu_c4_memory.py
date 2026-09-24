#!/usr/bin/env python3
"""Summarize finite mixed replay; sampled memory is not a leak proof."""
from datetime import datetime
import json
from pathlib import Path
import statistics

HERE = Path(__file__).resolve().parents[2] / 'lab-private/gpu-c4-paired-analysis-20260924'
HERE.mkdir(parents=True, exist_ok=True)
RUN = HERE.parent / 'gpu-c4-mixed-default-20260924'


def rows(name):
    return [json.loads(s) for s in (RUN / name).read_text().splitlines()]


def wall(s):
    return datetime.fromisoformat(s.replace('Z', '+00:00')).timestamp()


summary = json.loads((RUN / 'summary.json').read_text())
meta = json.loads((RUN / 'run.json').read_text())
assert summary['complete'] and summary['verified_measured'] == 45 and summary['verified_warmup'] == 9
assert meta['config']['msm_chunk_size'] == 1048576 and meta['config']['msm_internal_chunks'] == 4
assert meta['config']['interval'] == 2
metrics = rows('metrics.jsonl')
gpu = rows('gpu.jsonl')
external = rows('external.jsonl')
config = next(r for r in metrics if r['event'] == 'config')
# The observer starts immediately after receiving config. Discard a 0.5 s
# margin after each proof; don't pretend this is an exact cross-clock mapping.
offset = gpu[0]['monotonic_s'] - wall(config['time'])
proofs = [r for r in metrics if r['event'] == 'proof']
idle = []
for proof in proofs[:-1]:
    start = wall(proof['time']) + offset
    values = [sum(float(v) for v in r['process_used_mib']) for r in gpu
              if start + .5 <= r['monotonic_s'] <= start + 1.5 and r.get('process_used_mib')]
    if values:
        idle.append(dict(sequence=proof['context']['sequence'], warmup=proof['context']['warmup'],
                         samples=len(values), median_mib=statistics.median(values), max_mib=max(values)))

cycles = []
snapshots = [r for r in metrics if r['event'] == 'after_proof']
for cycle in range(6):
    selected = [r for r in snapshots if cycle * 9 < r['context']['sequence'] <= (cycle + 1) * 9]
    g = [r['median_mib'] for r in idle if cycle * 9 < r['sequence'] <= (cycle + 1) * 9]
    idle_host = [r for r in metrics if r['event'] == 'after_idle'
                 and cycle * 9 < r['context']['sequence'] <= (cycle + 1) * 9]
    cycles.append(dict(cycle=cycle, warmup=cycle == 0,
                       rss_min_bytes=min(r['rss_bytes'] for r in selected),
                       rss_max_bytes=max(r['rss_bytes'] for r in selected),
                       heap_alloc_min_bytes=min(r['heap_alloc'] for r in selected),
                       heap_alloc_max_bytes=max(r['heap_alloc'] for r in selected),
                       num_gc_start=selected[0]['num_gc'], num_gc_end=selected[-1]['num_gc'],
                       idle_rss_min_bytes=min(r['rss_bytes'] for r in idle_host),
                       idle_rss_max_bytes=max(r['rss_bytes'] for r in idle_host),
                       idle_gpu_min_mib=min(g), idle_gpu_max_mib=max(g)))
result = dict(complete=summary['complete'], verified_proofs=len(proofs),
              gpu=summary['gpu'], rss_hwm_bytes=summary['rss_hwm_bytes'],
              max_swap_bytes=max(r.get('swap_bytes', 0) for r in external),
              cycles=cycles, idle_gpu_samples_by_proof=idle,
              elapsed_s=wall(metrics[-1]['time']) - wall(config['time']),
              notes=['GPU idle windows use approximate clock alignment and exclude first 0.5 s after proof.',
                     'Two-second request intervals; no forced GC, no trim, no production workload.',
                     'Post-proof heap can include uncollected garbage; RSS alone is not retained object size.',
                     'Finite 54-proof replay cannot exclude small or long-term leaks.'])
(HERE / 'memory.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({k: v for k, v in result.items() if k != 'idle_gpu_samples_by_proof'}, indent=2))

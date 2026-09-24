#!/usr/bin/env python3
"""Analyze the fixed 36-proof GPU computeH interval replay from lab root."""
from datetime import datetime
import json
from pathlib import Path
import statistics

RUN=Path.cwd()/'lab-private/gpu-h-mixed-20260924'
def rows(name):return [json.loads(s) for s in (RUN/name).read_text().splitlines()]
def wall(s):return datetime.fromisoformat(s.replace('Z','+00:00')).timestamp()
def analyze():
    summary=json.loads((RUN/'summary.json').read_text());meta=json.loads((RUN/'run.json').read_text())
    assert summary['complete'] and summary['verified_measured']==27 and summary['verified_warmup']==9
    assert meta['config']['gpu_h'] and meta['config']['interval']==2 and not meta['config']['gc_between']
    metrics,gpu,external=rows('metrics.jsonl'),rows('gpu.jsonl'),rows('external.jsonl')
    assert not summary['gpu']['other_compute_processes_observed']
    config=next(r for r in metrics if r['event']=='config')
    offset=gpu[0]['monotonic_s']-wall(config['time'])
    proofs=[r for r in metrics if r['event']=='proof'];idle=[]
    for p in proofs[:-1]:
        begin=wall(p['time'])+offset
        window=[r for r in gpu if begin+.5<=r['monotonic_s']<=begin+1.5 and r.get('process_used_mib')]
        if window:
            idle.append(dict(sequence=p['context']['sequence'],process_median_mib=statistics.median(sum(float(v) for v in r['process_used_mib']) for r in window),device_median_mib=statistics.median(float(r['device']['memory.used']) for r in window)))
    assert len(idle)>=30,'insufficient idle windows'
    cycles=[]
    for i in range(4):
        host=[r for r in metrics if r['event']=='after_idle' and i*9<r['context']['sequence']<=(i+1)*9]
        card=[r for r in idle if i*9<r['sequence']<=(i+1)*9]
        cycles.append(dict(cycle=i,rss_min_bytes=min(r['rss_bytes'] for r in host),rss_max_bytes=max(r['rss_bytes'] for r in host),heap_min_bytes=min(r['heap_alloc'] for r in host),heap_max_bytes=max(r['heap_alloc'] for r in host),num_gc_start=host[0]['num_gc'],num_gc_end=host[-1]['num_gc'],idle_process_min_mib=min(r['process_median_mib'] for r in card),idle_process_max_mib=max(r['process_median_mib'] for r in card),idle_device_min_mib=min(r['device_median_mib'] for r in card),idle_device_max_mib=max(r['device_median_mib'] for r in card)))
    return dict(complete=True,verified_proofs=len(proofs),gpu=summary['gpu'],rss_hwm_bytes=summary['rss_hwm_bytes'],max_swap_bytes=max(r.get('swap_bytes',0) for r in external),cycles=cycles,idle_windows=idle,elapsed_s=wall(metrics[-1]['time'])-wall(config['time']),notes=['Two-second intervals, no forced GC or trim; original CPU verifier.', 'Idle windows exclude first 0.5s after proof; clock alignment and memory peaks are sampled.', 'Report NVML process and device use separately; managed memory accounting can differ.', 'A finite replay does not rule out every small or long-term leak.'])
if __name__=='__main__': print(json.dumps(analyze(),indent=2))

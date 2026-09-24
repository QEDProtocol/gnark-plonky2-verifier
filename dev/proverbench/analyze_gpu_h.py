#!/usr/bin/env python3
"""Reproduce the fixed-binary BAAB GPU computeH experiment (run from lab root)."""
import json
from pathlib import Path
import statistics

ROOT = Path.cwd() / 'lab-private'
ORDER = ['b1', 'a1', 'a2', 'b2']

def rows(path):
    return [json.loads(s) for s in path.read_text().splitlines()]

def stats(values):
    return dict(n=len(values), median=statistics.median(values), minimum=min(values), maximum=max(values))

def background(ext):
    valid = [r for r in ext if 'host_cpu_busy_s' in r]
    a,b = valid[0],valid[-1]
    own = b['user_cpu_s']+b['system_cpu_s']-a['user_cpu_s']-a['system_cpu_s']
    return max(0,b['host_cpu_busy_s']-a['host_cpu_busy_s']-own)/(b['monotonic_s']-a['monotonic_s'])

def analyze():
    runs,baseline,pool = {},None,{'a':{},'b':{}}
    for name in ORDER:
        run = ROOT / ('gpu-h-paired-'+name+'-20260924')
        meta=json.loads((run/'run.json').read_text())
        summary=json.loads((run/'summary.json').read_text())
        assert summary['complete'] and summary['verified_measured']==9 and summary['verified_warmup']==9
        assert not summary['gpu']['other_compute_processes_observed']
        assert meta['config']['gpu_h']==name.startswith('b')
        invariant={k:meta[k] for k in ['build','samples','input_sha256','host','kernel','cpu_model','affinity']}
        invariant['gpu']={k:v for k,v in meta['gpu'].items() if k not in ['memory.used','utilization.gpu']}
        invariant['config']={k:v for k,v in meta['config'].items() if k!='gpu_h'}
        if baseline is None:baseline=invariant
        assert invariant==baseline, name+' differs beyond gpu_h'
        metrics=rows(run/'metrics.jsonl');ext=rows(run/'external.jsonl')
        by_stage={}
        for scenario in ['bridge','deposit','withdrawal']:
            for stage in ['solver','compute_h','prover_compute','groth16_prove']:
                values=[r['duration_ms'] for r in metrics if r['event'] in ['stage','internal_stage'] and r.get('stage')==stage and r['context']['scenario']==scenario and not r['context']['warmup'] and r.get('ok',True)]
                assert len(values)==3
                key=scenario+'/'+stage
                by_stage[key]=stats(values)
                pool[name[0]].setdefault(key,[]).extend(values)
        details=[r for r in metrics if r['event']=='h_detail']
        if name.startswith('b'):
            assert len(details)==18 and all(r['ok'] for r in details)
        else:assert not details
        runs[name]=dict(stages=by_stage,gpu=summary['gpu'],rss_hwm_bytes=summary['rss_hwm_bytes'],max_swap_bytes=max(r.get('swap_bytes',0) for r in ext),estimated_background_cpu_cores=background(ext))
    comparison={}
    for key in pool['a']:
        a,b=stats(pool['a'][key]),stats(pool['b'][key])
        comparison[key]=dict(cpu_h=a,gpu_h=b,reduction_percent=100*(1-b['median']/a['median']))
    return dict(order=ORDER,verified_proofs=72,build=baseline['build'],runs=runs,comparison=comparison,
                notes=['Only gpu_h changes; both arms use GPU MSM with outer 1048576/internal 4.',
                       '18 proofs per run, 9 warmups excluded; each configuration/scenario has 6 measured samples.',
                       'Same original parameters and CPU verifier; not full Rust/FFI/RPC latency.',
                       'GPU memory peaks are sampled; shared host and finite replay limit generalization.'])

if __name__=='__main__':
    print(json.dumps(analyze(),indent=2))

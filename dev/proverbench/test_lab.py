"""Run later with python3 -m unittest discover -s dev/proverbench -v."""
import unittest
from lab import select_samples, summarize


class LabTests(unittest.TestCase):
    def test_bad_manifest(self):
        for name in ('../x', '..', '.', '', '/x', 'a\\b'):
            with self.assertRaises(ValueError):
                select_samples({'ffi_samples': [{'id': name, 'scenario': 'bridge'}]}, 'all', '')

    def test_summary_excludes_warmup_and_failed_request(self):
        def event(kind, seq=0, warm=False, **kwargs):
            return {'event': kind, 'context': {'sequence': seq, 'warmup': warm, 'scenario': 'bridge'}, **kwargs}
        events = [event('proof', 1, True, verified=True, duration_ms=999),
                  event('stage', 2, stage='verify', ok=True, duration_ms=1),
                  event('proof', 2, verified=True, duration_ms=10),
                  event('finished')]
        meta = {'config': {'cycles': 1, 'warmup': 1}, 'samples': [{'id': 'bridge-00'}]}
        result = summarize(events, meta, 0)
        self.assertTrue(result['complete'])
        self.assertEqual(result['stages']['bridge/total']['median_ms'], 10)
        self.assertFalse(summarize(events[:-1], meta, 0)['complete'])
        self.assertFalse(summarize(events, meta, 1)['complete'])
        events.extend([event('stage', 3, stage='groth16_prove', ok=False, duration_ms=500),
                       {'event': 'error', 'context': {}, 'code': 'non_metric_stderr_redacted'}])
        result = summarize(events, meta, 1)
        self.assertFalse(result['complete'])
        self.assertNotIn('bridge/groth16_prove', result['stages'])


if __name__ == '__main__':
    unittest.main()

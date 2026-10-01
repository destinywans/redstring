import copy,json,sys,unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rule_create_functional_coverage as create
class CreateTests(unittest.TestCase):
    def test_machine_contract_exact_operations(self):
        c=create.Contract(create.ROOT)
        self.assertEqual(c.canonical('POST','/api/v1/alert-rules'),('/api/v1/alert-rules','main'))
        self.assertEqual(c.canonical('GET','/api/v1/alert-rules/dry-runs/'+'a'*32)[0],'/api/v1/alert-rules/dry-runs/{id}')
        for m,p in [('POST','/api/v1/alert-rules/import'),('GET','/api/v1/alert-rules')]:
            with self.assertRaises(create.Blocked):c.canonical(m,p)
    def value(self):return dict(trial_id='a'*32,status='running',started_at='2026-10-01T00:00:00Z',expires_at='2026-10-02T00:00:00Z',last_update='2026-10-01T00:00:01Z',evaluated_points=1,missing_points=0,would_fire=1,windows=['x'])
    def test_exact_86400_window(self):
        create.validate_trial(self.value(),'a'*32)
        for patch in [dict(expires_at='2026-10-01T00:01:00Z'),dict(status='completed'),dict(evaluated_points=True),dict(trial_id='other'),dict(secret='bad')]:
            v=dict(self.value(),**patch)
            with self.assertRaises(AssertionError):create.validate_trial(v)
    def test_queued_nulls(self):
        v=dict(self.value(),status='queued',started_at=None,expires_at=None,evaluated_points=0);create.validate_trial(v)
        v['expires_at']='2026-10-02T00:00:00Z'
        with self.assertRaises(AssertionError):create.validate_trial(v)
    def test_spec_matches_shape_and_scoped_labels(self):
        v=create.spec('a'*32,'b'*32,'metric > 0');self.assertEqual(v['mode'],'dry_run');self.assertEqual(v['for'],'0s');self.assertEqual(v['labels']['application'],v['application_id']);self.assertEqual(v['environment_id'],v['labels']['environment'])
        with self.assertRaises(AssertionError):create.spec('wrong','b'*32,'metric > 0')
    def test_context_restores_all_shared_objects(self):
        before=(create.rules.PORTS,create.rules.Source,create.ro.emit,create.ro.transport,create.rules.hashes)
        with self.assertRaises(RuntimeError):
            with create.context():
                self.assertEqual(create.rules.PORTS,(18680,18681));raise RuntimeError('controlled')
        self.assertEqual(before,(create.rules.PORTS,create.rules.Source,create.ro.emit,create.ro.transport,create.rules.hashes))
if __name__=='__main__':unittest.main()

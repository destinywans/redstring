import sys,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from store import Store
from prepare import prepare_all

class PrepareTests(unittest.TestCase):
 def test_refuses_to_restart_active_run(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp);s=Store(root);r=s.create_run();s.update('runs',r['id'],status='running')
   with patch('backend.ROOT',root),patch('perf.ROOT',root),patch('env.prepare') as prepare:
    with self.assertRaisesRegex(RuntimeError,'tests are running'):prepare_all(root,'testlab-own')
    prepare.assert_not_called()

 def test_wrong_checkout_refused_before_any_environment_or_ledger_creation(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp)
   with patch('env.prepare') as prepare,patch('components.prepare') as components,patch('backend.setup') as backend,patch('perf.prepare') as perf:
    with self.assertRaisesRegex(ValueError,'checkout'):prepare_all(root,'unrelated')
    prepare.assert_not_called();components.assert_not_called();backend.assert_not_called();perf.assert_not_called()
   self.assertFalse((root/'.local').exists())

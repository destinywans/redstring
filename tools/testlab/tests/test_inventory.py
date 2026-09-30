import sys,unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from inventory import actual_interface,interfaces

class InventoryTests(unittest.TestCase):
 def test_auth_response_is_not_registration_evidence(self):
  row=dict(method='GET',path='/api/v1/test',scenario='authorization',status='passed',expected=401,actual=401,registration_evidence=True)
  result=actual_interface(row)
  self.assertFalse(result['registration_evidence'])
  self.assertEqual(set(result['scenarios']),{'authorization'})
 def test_inventory_is_unverified_source(self):
  rows=list(interfaces(Path(__file__).resolve().parents[3]))
  self.assertEqual(len({(r['method'],r['path']) for r in rows}),len(rows))
  self.assertGreater(len(rows),400)
  self.assertTrue(all(not r['registration_evidence'] and {v['status'] for v in r['scenarios'].values()}=={'queued'} for r in rows))
  self.assertTrue(all(r['path'].startswith('/api/v1/') for r in rows))

import contextlib,copy,io,json,sys,unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_lower_sources_functional_coverage as lower
class LowerSourcesTests(unittest.TestCase):
 def test_inventory_no_resources(self):
  with patch.object(lower.ro,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(lower.main(['--inventory']),0)
  self.assertEqual(json.loads(out.getvalue())['targets'],[['GET','/integrations/lower-sources']]);doctor.assert_not_called()
 def test_exact_dto_rejects_secret_order_and_verified_claim(self):
  for lang in ('en-US','zh-CN'):
   lower.verify(lower.expected(lang),lang)
   for mutation in ('endpoint','order','claim','empty'):
    value=copy.deepcopy(lower.expected(lang))
    if mutation=='endpoint':value['items'][0]['endpoint']='secret'
    elif mutation=='order':value['items'].reverse()
    elif mutation=='claim':value['items'][0]['credential']='connected'
    else:value['items']=[]
    with self.assertRaises(AssertionError):lower.verify(value,lang)
 def test_only_read_metadata_or_login(self):
  c=lower.Contract(lower.ROOT)
  self.assertEqual(c.canonical('GET','/api/v1/integrations/lower-sources')[1],'main')
  for m,p in [('POST','/api/v1/integrations/lower-sources'),('GET','/api/v1/config-sync'),('GET','https://foreign/api/v1/integrations/lower-sources')]:
   with self.assertRaises(lower.Blocked):c.canonical(m,p)
 def test_context_restores_shared_values(self):
  before=lower.ro.PORTS,lower.ro.BASES,lower.ro.emit,lower.ro.transport
  with self.assertRaises(RuntimeError),lower.context():raise RuntimeError()
  self.assertEqual(before,(lower.ro.PORTS,lower.ro.BASES,lower.ro.emit,lower.ro.transport))
if __name__=='__main__':unittest.main()

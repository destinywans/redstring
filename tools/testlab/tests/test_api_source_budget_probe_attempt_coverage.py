"""Offline seams only; no HTTP, PG, Docker, listeners or native execution."""
import collections, contextlib, copy, hashlib, importlib.util, io, json, pathlib, sys, types, unittest
from unittest.mock import Mock, patch

ROOT=pathlib.Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tools/testlab'))
spec=importlib.util.spec_from_file_location('offline_source_budget_probe',ROOT/'tools/testlab/api_source_budget_probe_attempt_coverage.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
GO_DIGEST_BYTES=b'{"expression":"owned","name_labels":["service"],"label_keys":["signal_id"]}'
GO_DIGEST_SHA='e6519c616f1ff88df518f747361a5e119b79d3e793620aaec1072c1db8c2e606'
OLD_UPPERCASE_SHA='497494597a83160148d094791e66ff645c156c8b9f83239048d83bb60924a600'

def identity(c):
 assert c.expression=='owned'
 return dict(source_id=c.source_id,application_id=c.app,environment_id='test',connector_id=c.connector,target_id=c.target,resource_id=c.resource_id,query_id='capacity_signal',config_digest=GO_DIGEST_SHA)

def completed(c,actors):
 return dict(identities=[identity(c)],attempts=[dict(id=i+1,source_id=c.source_id,actor_id=a,kind='preview',status='success',rows_count=1,dropped_count=0,error_code='') for i,a in enumerate(actors)],audit=[dict(actor_id=a,application_id=c.app,action='v3.ingest_pull.preview',target=c.source_id,outcome='success',changes=dict(source_id=c.source_id,environment_id='test',kind='preview',rows=1,dropped=0,error_code='')) for a in actors])

def budget_chain():
 c=object.__new__(m.Chain);c.source_id='source';c.app='app';c.connector='connector';c.target='target';c.resource_id='resource';c.expression='owned';c.work=pathlib.Path('/offline');c.admin_actor='9'*32
 c.users={name:str(i+1)*32 for i,name in enumerate(('reader','peer','hidden','denied','budget_a','budget_b'))};c.frozen=Mock();c.marker_case=Mock();c.responses={};c.gate=types.SimpleNamespace(release=Mock(),arrivals=[],receipts={});c.requested=[]
 actors=[c.actor_id(a) for a in m.BUDGET_ACTORS]*10;full=completed(c,actors)
 c.ledger=Mock(side_effect=[dict(attempts=[],identities=[],audit=[]),full,copy.deepcopy(full),completed(c,actors+[c.actor_id(m.REJECT_ACTOR)])])
 c.sql=Mock(side_effect=[json.dumps(dict(total=60,window=60,new_actor=0,pending=0,oldest_age_seconds=3)),json.dumps(59.9),json.dumps(60.1)])
 def request(key,user,index,timeout):
  c.requested.append((key,user,index,timeout))
  if key!='budget-61':
   n=len(c.gate.arrivals);c.gate.arrivals.append(n);c.gate.receipts[n]={'status':200};c.responses[key]={'rows':1}
 c.request=request;c.record=Mock();return c

def probe_chain():
 c=object.__new__(m.Chain);c.users={'reader':'a'*32};c.app='app';c.work=pathlib.Path('/offline');c.probe_payload={'type':'upload','url':'/api/v1/uploads/owned/content','application_id':'app'}
 before=dict(business={'uploads':'same','v3_pull_source_identities':'same'},audits=[])
 audits=[dict(id=i+1,actor_id=c.users['reader'],actor_kind='user',application_id=c.app,action='v3.knowledge_source.connection.test',target='upload',outcome=outcome,changes={'environment_id':'test','team_id':''}) for i,outcome in enumerate(('attempted','success'))]
 c.probe_ledger=Mock(side_effect=[before,copy.deepcopy(before),dict(business=before['business'],audits=audits)]);c.install_probe_fault=Mock();c.remove_probe_fault=Mock();c.call=Mock();c.marker_case=Mock();return c

class PackageTests(unittest.TestCase):
 def test_inventory_no_acquisition_and_exact_budget(self):
  with patch.object(m.cap.subprocess,'run',side_effect=AssertionError('no resource')),patch.object(m.socket,'socket',side_effect=AssertionError('no socket')),patch.object(m.cap,'doctor',side_effect=AssertionError('no doctor')),patch.object(m,'admission',side_effect=AssertionError('no admission')),contextlib.redirect_stdout(io.StringIO()) as out:
   self.assertEqual(m.main(['--inventory']),0)
  data=json.loads(out.getvalue());self.assertEqual(data['http_calls'],75);self.assertEqual(data['pull_calls'],62);self.assertEqual(len(data['required_cases']),14)
  self.assertEqual(data['ports'],list(range(18892,18897)));self.assertEqual((data['API_seconds'],data['provider_seconds'],data['SQL_milliseconds'],data['outer_seconds'],data['cleanup_seconds'],data['grace_seconds']),(12,5,8000,300,40,45))
  self.assertEqual(data['normal_added'],0);self.assertFalse(data['upload_read_invocations_observed'])
 def test_missing_live_admission_never_acquires(self):
  with patch.object(m.cap,'doctor',side_effect=AssertionError('no acquire')),contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):m.main(['--live'])
 def test_canonical_admission_rejects_external_path_before_read(self):
  with patch.object(m.ro,'private_json',side_effect=AssertionError('must not read')),self.assertRaises(AssertionError):m.admission('/tmp/not-admitted.json','11111111-1111-4111-8111-111111111111')
 def test_contract_only_two_targets_and_fixtures(self):
  c=m.Contract(ROOT);self.assertEqual(c.canonical('GET','/api/v1/ingest/pull/source')[0],'/api/v1/ingest/pull/{id}');c.canonical('POST','/api/v1/knowledge-sources/test');c.canonical('POST','/api/v1/uploads')
  for method,path in [('POST','/api/v1/ingest/pull/source/test'),('POST','/api/v1/incidents'),('GET','/api/v1/ingest/pull/source?x=1'),('POST','https://other/api/v1/uploads')]:
   with self.subTest(path=path),self.assertRaises(AssertionError):c.canonical(method,path)
 def test_fault_exact_first_attempted_scope_and_drop(self):
  name,create,drop=m.fault_statements('1'*32,'2'*32,'owned-app')
  for text in ("NEW.actor_id='"+'2'*32+"'","NEW.application_id='owned-app'","NEW.outcome='attempted'","NEW.target='upload'","'environment_id','test','team_id',''","BEFORE INSERT ON public.management_audit"):self.assertIn(text,create)
  self.assertNotIn("NEW.outcome='success'",create);self.assertIn('DROP TRIGGER IF EXISTS '+name+' ON public.management_audit',drop);self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()',drop)
  for nonce,actor,app in [('bad','2'*32,'app'),('1'*32,'other','app'),('1'*32,'2'*32,"app';DROP")]:
   with self.assertRaises(AssertionError):m.fault_statements(nonce,actor,app)
 def test_source_identity_includes_digest_and_original_binding(self):
  c=budget_chain();good=identity(c);m.source_identity([good],c)
  for key in ('source_id','application_id','environment_id','connector_id','target_id','resource_id','query_id','config_digest'):
   bad=dict(good,**{key:'foreign'})
   with self.subTest(key=key),self.assertRaises(AssertionError):m.source_identity([bad],c)
  with self.assertRaises(AssertionError):m.source_identity([good,good],c)
 def test_source_digest_real_go_lowercase_literal(self):
  self.assertEqual(hashlib.sha256(GO_DIGEST_BYTES).hexdigest(),GO_DIGEST_SHA)
  source=(ROOT/'internal/v3ingestpull/service.go').read_text();start=source.index('func sourceDigest(');digest_code=source[start:source.index('func (s *Service) reserve',start)]
  for tag in ('`json:"expression"`','`json:"name_labels"`','`json:"label_keys"`'):self.assertIn(tag,digest_code)
  c=budget_chain();row=identity(c);row['config_digest']=GO_DIGEST_SHA;m.source_identity([row],c)
 def test_source_digest_old_uppercase_is_rejected(self):
  self.assertEqual(hashlib.sha256(b'{"Expression":"owned","NameLabels":["service"],"LabelKeys":["signal_id"]}').hexdigest(),OLD_UPPERCASE_SHA)
  c=budget_chain();row=identity(c);row['config_digest']=OLD_UPPERCASE_SHA
  with self.assertRaises(AssertionError):m.source_identity([row],c)
 def test_window_requires_all60_real_recent_and_seventh_actor_empty(self):
  good=dict(total=60,window=60,new_actor=0,pending=0,oldest_age_seconds=1);m.budget_window(good)
  for key,value in [('total',59),('window',59),('new_actor',1),('pending',1),('oldest_age_seconds',45),('oldest_age_seconds',-1),('oldest_age_seconds',True)]:
   with self.subTest(key=key,value=value),self.assertRaises(AssertionError):m.budget_window(dict(good,**{key:value}))
 def test_budget60_reject61_then_one_natural_recovery62(self):
  c=budget_chain()
  with patch.object(m.time,'monotonic',return_value=0),patch.object(m.time,'sleep') as sleep,patch.object(m.cap,'private_json'):c.source_budget()
  self.assertEqual(len(c.requested),62);self.assertEqual(collections.Counter(r[1] for r in c.requested[:60]),collections.Counter({a:10 for a in m.BUDGET_ACTORS}));self.assertEqual(c.requested[60:], [('budget-61','budget_b',1,12),('budget-62','budget_b',0,12)])
  self.assertEqual(c.record.call_args_list[60].args,('budget-61',429));self.assertEqual(c.record.call_args_list[61].args[0:2],('budget-62',200));self.assertEqual(len(c.gate.arrivals),61);self.assertEqual(len(c.gate.receipts),61);self.assertTrue(all(a.args[0]<=5 for a in sleep.call_args_list));self.assertEqual(c.sql.call_count,3)
  self.assertIn('source_recovery',[a.args[0] for a in c.marker_case.call_args_list])
 def test_rejected_call_must_not_add_upstream_read(self):
  c=budget_chain();original=c.request
  def request(*args,**kwargs):
   original(*args,**kwargs)
   if args[0]=='budget-61':c.gate.arrivals.append('unexpected')
  c.request=request
  with patch.object(m.time,'monotonic',return_value=0),patch.object(m.cap,'private_json'),self.assertRaises(AssertionError):c.source_budget()
  self.assertEqual(len(c.requested),61);self.assertNotIn('budget-62',[r[0] for r in c.requested])
 def test_rejected_audit_or_attempt_change_stops_before_cooldown(self):
  c=budget_chain();ledgers=list(c.ledger.side_effect);ledgers[2]['audit'].append({});c.ledger.side_effect=ledgers
  with patch.object(m.time,'monotonic',return_value=0),patch.object(m.cap,'private_json'),self.assertRaises(AssertionError):c.source_budget()
  self.assertEqual(len(c.requested),61);self.assertEqual(c.sql.call_count,1)
 def test_failed_first_success_is_never_retried(self):
  c=budget_chain();c.record.side_effect=AssertionError('wrong response')
  with patch.object(m.time,'monotonic',return_value=0),self.assertRaises(AssertionError):c.source_budget()
  self.assertEqual(len(c.requested),1)
 def test_elapsed_window_stops_before_first_target(self):
  c=budget_chain()
  with patch.object(m.time,'monotonic',side_effect=[0,45]),self.assertRaises(AssertionError):c.source_budget()
  self.assertEqual(c.requested,[])
 def test_cooldown_timeout_stops_without_recovery_or_retry(self):
  c=budget_chain();c.sql.side_effect=[json.dumps(dict(total=60,window=60,new_actor=0,pending=0,oldest_age_seconds=3)),json.dumps(59)]
  ticks=collections.Counter()
  def clock():ticks['calls']+=1;return 0 if ticks['calls']<=65 else 70
  with patch.object(m.time,'monotonic',side_effect=clock),patch.object(m.time,'sleep'),patch.object(m.cap,'private_json'),self.assertRaises(AssertionError):c.source_budget()
  self.assertEqual(len(c.requested),61)
 def test_probe_actual_method_restores_and_reuses_identical_body(self):
  c=probe_chain()
  with patch.object(m.cap,'private_json'):c.probe_attempt()
  self.assertEqual(c.call.call_count,2);first,last=c.call.call_args_list;self.assertIs(first.args[3],last.args[3]);self.assertEqual(first.kwargs['code'],'audit_unavailable');self.assertEqual(first.kwargs['want'],503);self.assertEqual((first.kwargs['index'],last.kwargs['index']),(0,1));c.remove_probe_fault.assert_called_once()
  no_residue=next(call.args[1] for call in c.marker_case.call_args_list if call.args[0]=='probe_no_residue');self.assertFalse(no_residue['upload_read_invocations_observed']);self.assertFalse(no_residue['external_credential_resolution_applicable'])
 def test_probe_unexpected_first_response_stops_after_restore(self):
  c=probe_chain();first=RuntimeError('private text');c.call.side_effect=first
  with self.assertRaises(RuntimeError) as seen:c.probe_attempt()
  self.assertIs(seen.exception,first);self.assertEqual(c.call.call_count,1);c.remove_probe_fault.assert_called_once()
 def test_probe_business_residue_is_not_washed_by_recovery(self):
  c=probe_chain();ledgers=list(c.probe_ledger.side_effect);ledgers[1]['business']['uploads']='changed';c.probe_ledger.side_effect=ledgers
  with self.assertRaises(AssertionError):c.probe_attempt()
  self.assertEqual(c.call.call_count,1);c.remove_probe_fault.assert_called_once()
 def test_probe_first_error_preserved_over_removal_error(self):
  c=probe_chain();first=RuntimeError('private');c.call.side_effect=first;c.remove_probe_fault.side_effect=ValueError('secondary private')
  with self.assertRaises(RuntimeError) as seen:c.probe_attempt()
  self.assertIs(seen.exception,first);self.assertEqual(first.__notes__,['owned attempted-audit removal secondary: ValueError']);self.assertEqual(c.call.call_count,1)
 def test_cleanup_one40_budget_and_preserves_primary(self):
  c=object.__new__(m.Chain);c.cleanup_deadline=None;first=RuntimeError('primary');c.remove_probe_fault=Mock(side_effect=first);c.marker_case=Mock()
  with patch.object(m.time,'monotonic',return_value=100),patch.object(m,'bounded_cleanup',side_effect=ValueError('secondary')) as cleanup,self.assertRaises(RuntimeError) as seen:c.cleanup()
  self.assertIs(seen.exception,first);self.assertEqual(c.cleanup_deadline,140);c.remove_probe_fault.assert_called_once_with(deadline=140);cleanup.assert_called_once_with(c);c.marker_case.assert_not_called();self.assertEqual(first.__notes__,['owned cleanup secondary: ValueError'])
 def test_output_has_only_safe_error_type(self):
  with contextlib.redirect_stdout(io.StringIO()) as out:m.emit('offline','failed',dict(error='PRIVATE',stderr='PRIVATE',token='PRIVATE',cookie='PRIVATE',headers='PRIVATE',error_type='ValueError'))
  self.assertNotIn('PRIVATE',out.getvalue());self.assertEqual(json.loads(out.getvalue())['actual'],{'error_type':'ValueError'})
 def test_container_receipt_drops_arbitrary_output_text(self):
  with patch.object(m,'_private_json') as save:m.private_json(pathlib.Path('/offline/container-inspect-cleanup-before.json'),dict(target='owned',phase='cleanup-before',returncode=1,stdout='PRIVATE',stderr='PRIVATE'))
  self.assertEqual(save.call_args.args[1],dict(target='owned',phase='cleanup-before',returncode=1))
 def test_frozen_product_control_flow_and_real_clock_contract(self):
  pull=(ROOT/'internal/v3ingestpull/service.go').read_text();registry=(ROOT/'internal/v3sourceregistry/service.go').read_text()
  self.assertIn('sourceCount >= 60 || actorCount >= 10 || concurrent >= 4',pull);self.assertIn("started_at>clock_timestamp()-interval '1 minute'",pull);self.assertIn('"Retry-After", "60"',pull)
  start=registry.index('func (s *Service) testConnection');route=registry[start:];audit=route.index('"attempted"');reader=route.index('prober.Probe');self.assertLess(audit,reader);self.assertIn('return',route[audit:reader]);self.assertIn('audit_unavailable',route[audit:reader])

if __name__=='__main__':unittest.main()

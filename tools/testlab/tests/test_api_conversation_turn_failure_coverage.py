"""Offline durable failure/recovery, actual call seam and retained cursor counterexamples.
No listener, HTTP, model, PG, Docker or native execution.
"""
import contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_conversation_turn_failure_coverage as c
CID='c'*32;ACTOR='own-actor';TITLE='own-title'
def ledgers():
 created={t:[] for t in c.TABLES};created.update(business={'other':'stable'});conv=dict(id=CID,owner_id=ACTOR,environment_id='test',active_reply_id=None,metrics_source_required=False);created['v3_conversations']=[conv];created['management_audit']=[dict(id=1,action='v3.conversation.create')];after=copy.deepcopy(created);turns=[]
 for n,state in enumerate(('failed','completed')):
  rid=str(n+1)*32;uid=str(n+3)*32;aid=str(n+5)*32;text='own-'+state;turns.append((rid,uid,state,text));after['v3_conversation_replies'].append(dict(id=rid,conversation_id=CID,user_message_id=uid,assistant_message_id=aid,state=state,model_id=c.MODEL,reasoning='',finished_at='own-time',lease_expires_at=None,error='own HTTP500' if n==0 else ''))
  parts=[dict(type='text',markdown=''),dict(type='status',text='Model reply failed',detail='Retry this message after checking the model connection.')] if n==0 else [dict(type='text',markdown=c.ANSWER)]
  after['v3_conversation_messages'] += [dict(id=uid,conversation_id=CID,role='user',parts=[dict(type='text',markdown=text)],state='completed'),dict(id=aid,conversation_id=CID,role='assistant',parts=parts,state=state)]
  payloads=[dict(type='message.started',message=dict(id=aid)),dict(type='part.added',message_id=aid,part=dict(type='text',markdown=''))]
  payloads.append(dict(type='part.added',message_id=aid,part=parts[-1]) if n==0 else dict(type='text.delta',message_id=aid,delta=c.ANSWER,index=0));payloads.append(dict(type='message.completed',message_id=aid))
  for payload in payloads:after['v3_conversation_events'].append(dict(seq=len(after['v3_conversation_events'])+1,conversation_id=CID,reply_id=rid,payload=payload))
  after['management_audit'] += [dict(actor_id=ACTOR,action='v3.conversation.message',target=CID,outcome='accepted',changes=dict(reply_id=rid,model_id=c.MODEL,environment='test',reasoning='')),dict(actor_id=ACTOR,actor_kind='system',action='v3.conversation.reply',target=rid,outcome=state,changes=dict(conversation_id=CID))]
 return created,after,turns
def wire(events,cursor=0):return ''.join('id: '+str(e['seq'])+'\ndata: '+json.dumps(e['payload'])+'\n\n' for e in events if e['seq']>cursor).encode()
class TurnTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.users={'reader':ACTOR};x.cookies={'reader':'memory-cookie-do-not-store'};x.calls=0;x.frozen=Mock();x.first_red=Mock();x.marker_case=Mock();x.contract=c.Contract(c.ROOT);return x
 def test_real_compiled_accepted_call_validator_before_emit_and_both_transport_restore(self):
  x=self.chain();good=dict(message=dict(id='1'*32,role='user',parts=[dict(type='text',markdown='owned')]),reply_id='2'*32);fake=Mock(return_value=(202,json.dumps(good).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
   x.call('reader','POST','/api/v1/conversations/'+CID+'/messages',dict(text='owned',model=c.MODEL),want=202,validate=lambda v:c.accepted(v,'owned'));self.assertIs(c.base.transport,fake);self.assertIs(c.h._namespace['transport'],fake)
  fake.assert_called_once();x.first_red.assert_not_called();self.assertTrue(all(not json.loads(r)['interface']['registration_evidence'] for r in out.getvalue().splitlines()));self.assertNotIn('memory-cookie',(self.work/'conversation-wire-1.json').read_text())
 def test_accepted_schema_valid_wrong_text_is_firstred_not_pass(self):
  x=self.chain();fake=Mock(return_value=(202,json.dumps(dict(message=dict(id='1'*32,role='user',parts=[dict(type='text',markdown='wrong')]),reply_id='2'*32)).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out,self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/conversations/'+CID+'/messages',dict(text='owned'),want=202,validate=lambda v:c.accepted(v,'owned'))
  x.first_red.assert_called_once();self.assertTrue(all(json.loads(r)['status']=='failed' for r in out.getvalue().splitlines()))
 def test_terminal_ledger_two_real_shapes_and_failures_do_not_silently_complete(self):
  created,after,turns=ledgers();c.verify_ledger(created,after,CID,ACTOR,turns)
  mutations=[lambda a:a['v3_conversations'][0].update(active_reply_id=turns[-1][0]),lambda a:a['v3_conversation_replies'][0].update(state='completed'),lambda a:a['v3_conversation_replies'][1].update(error='failure'),lambda a:a['v3_conversation_messages'].pop(),lambda a:a['management_audit'].pop(),lambda a:a['business'].update(other='changed'),lambda a:a['v3_metrics_questions'].append({'source':'fabricated'}),lambda a:a['v3_conversation_events'][0].update(reply_id='foreign'),lambda a:a['v3_conversation_events'][0]['payload'].update(type='message.failed'),lambda a:a['v3_conversation_events'][6]['payload'].update(delta='lost'),lambda a:a['management_audit'][1]['changes'].update(reply_id='foreign')]
  for mutate in mutations:
   bad=copy.deepcopy(after);mutate(bad)
   with self.assertRaises(AssertionError):c.verify_ledger(created,bad,CID,ACTOR,turns)
 def test_cursor_full_suffix_and_terminal_empty_against_persisted_events(self):
  events=c.reply_events(ledgers()[1],'1'*32)
  for cursor in (0,events[0]['seq'],events[-1]['seq']):c.verify_replay(200,{'Content-Type':'text/event-stream'},wire(events,cursor),events,cursor)
  for raw in (wire(events[1:]),wire(events)+wire(events[-1:]),wire(events[1:])+wire(events[:1]),wire(events).replace(b'message.completed',b'message.failed')):
   with self.assertRaises(AssertionError):c.verify_replay(200,{'Content-Type':'text/event-stream'},raw,events,0)
  with self.assertRaises(AssertionError):c.verify_replay(200,{'Content-Type':'application/json'},wire(events),events,0)
 def test_actual_replay_branch_emits_unqualified_and_preserves_ledger_and_counter(self):
  x=self.chain();ledger=ledgers()[1];events=c.reply_events(ledger,'1'*32);x.ledger=Mock(return_value=ledger);x.provider=Mock();x.provider.snapshot.return_value={'records':[{'status':500}]};fake=Mock(side_effect=[(200,wire(events,k),{'Content-Type':'text/event-stream'}) for k in (0,events[0]['seq'],events[-1]['seq'])])
  with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:x.replay(CID,'1'*32,ledger,1)
  self.assertEqual(x.calls,3);self.assertEqual(fake.call_count,3);self.assertTrue(all(json.loads(r)['interface']['registration_evidence'] is False for r in out.getvalue().splitlines()));self.assertNotIn('Cookie',''.join(p.read_text() for p in self.work.iterdir()))
 def test_replay_wrong_body_or_side_effect_preserves_firstred_and_no_retry(self):
  ledger=ledgers()[1];events=c.reply_events(ledger,'1'*32)
  for bad_body,bad_ledger,changed_counter in ((True,False,False),(False,True,False),(False,False,True)):
   x=self.chain();bad=copy.deepcopy(ledger);bad['management_audit'].append({'unexpected':'side effect'});x.ledger=Mock(return_value=bad if bad_ledger else ledger);x.provider=Mock();x.provider.snapshot.side_effect=[{}, {'changed':True}] if changed_counter else None;x.provider.snapshot.return_value={};fake=Mock(return_value=(200,wire(events[1:] if bad_body else events),{'Content-Type':'text/event-stream'}))
   with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.replay(CID,'1'*32,ledger,1)
   x.first_red.assert_called_once();fake.assert_called_once()
 def test_terminal_wrong_state_or_active_pointer_stops_without_resend(self):
  for value in (dict(state='completed',active=None),dict(state='failed',active='still-active')):
   x=self.chain();x.sql=Mock(return_value=json.dumps(value))
   with self.assertRaises(AssertionError):x.terminal(CID,'1'*32,'failed')
   x.sql.assert_called_once()
 def test_detail_requires_all_failed_and_completed_parts_not_only_200(self):
  ledger=ledgers()[1];dto=dict(id=CID,title=TITLE,scope=dict(env='test',inferred=False),status='active',mode='chat',artifacts=[],messages=[dict(id=m['id'],role=m['role'],parts=m['parts'],state=m['state']) for m in ledger['v3_conversation_messages']]);c.verify_detail(dto,CID,TITLE,ledger)
  for key in ('parts','state','streaming'):
   bad=copy.deepcopy(dto);bad['messages'][1][key]=True if key=='streaming' else ('completed' if key=='state' else [])
   with self.assertRaises(AssertionError):c.verify_detail(bad,CID,TITLE,ledger)
 def test_provider_recovery_is_explicit_only_after_one_failed_request(self):
  p=c.Provider.__new__(c.Provider);p.lock=c.threading.Lock();p.mode='failed';p.errors=[];p.records=[]
  with self.assertRaises(AssertionError):p.recover()
  p.records=[dict(status=500)];p.recover();self.assertEqual(p.mode,'completed');self.assertEqual(json.loads(c.provider_response(500))['error']['message'],'owned controlled provider failure');self.assertIn(c.ANSWER.encode(),c.provider_response(200));self.assertTrue(c.provider_response(200).endswith(b'data: [DONE]\n\n'))
 def test_provider_cleanup_failure_still_calls_atomic_pg_cleanup_same_deadline(self):
  x=self.chain();x.provider=Mock();x.provider.close.side_effect=AssertionError('owned-worker-incomplete')
  with patch.object(c,'_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
   sock.return_value.__enter__.return_value.connect_ex.return_value=1
   with self.assertRaises(AssertionError):x.cleanup()
  cleanup.assert_called_once_with(x);self.assertLessEqual(x.provider.close.call_args.args[0],x.cleanup_deadline);self.assertGreater(x.cleanup_deadline-c.time.monotonic(),35)
 def test_inventory_no_lifecycle_unique_markers_protocol_mock(self):
  with patch.object(c,'doctor') as doctor,patch.object(c,'Provider') as provider,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  value=json.loads(out.getvalue());doctor.assert_not_called();provider.assert_not_called();self.assertEqual(value['estimated_HTTP_calls'],13);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)));self.assertEqual(value['normal_added'],0);self.assertFalse(value['registration_evidence']);self.assertEqual(value['cleanup_seconds'],40)
if __name__=='__main__':unittest.main()

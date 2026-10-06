"""Two durable ordinary turns: controlled protocol HTTP500, then explicit recovery.
Prepared only unless root supplies fresh admission. No real-model qualification.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap,threading,time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-conversation-turn-failure-recovery';PORTS=(18872,18873);PROVIDER_PORT=18874;ALL_PORTS=(*PORTS,PROVIDER_PORT)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb';RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d';PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
BASE_HELPER_SHA='6501cb3c5e9603d8071731b963eb92e19c1a65d286326ecff8639e3bfd829d11';ANSWER='Owned recovered protocol reply.'
s=importlib.util.spec_from_file_location('turn_private_conversation',ROOT/'tools/testlab/api_conversation_object_session_coverage.py');h=importlib.util.module_from_spec(s);s.loader.exec_module(h)
base=h.base;check,Blocked,q=h.check,h.Blocked,h.q;MODEL=h.MODEL;TABLES=(*h.TABLES,'v3_metrics_questions');h.TABLES=TABLES;h.MODULE=MODULE;h.PORTS=PORTS;base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);h._namespace['BASES']=base.BASES
TARGETS=h.TARGETS;SUPPORT=(('POST','/conversations'),('GET','/models'),('POST','/auth/login'))
REQUIRED_CASES=tuple('conversation_turns.'+n for n in ('identity','provider','created','failed','failed_replay','recovery','completed','completed_replay','detail','ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_conversation_turn_failure_coverage.py','tests/test_api_conversation_turn_failure_coverage.py','api_conversation_object_session_coverage.py','tests/test_api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='protocol_mock',upstream_mode='protocol_mock',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalConversationTurnFailureRecovery',proof_scope='formal durable ordinary turn failure and explicit recovery, retained SSE replay; controlled model protocol Mock, no real inference or whole acceptance')
h.evidence=evidence
class Contract(h.Contract):
 def canonical(self,method,path):
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside turn scope');return canonical,kind
base.Contract=Contract
def adapt(fn,replacements,scope):
 source=inspect.getsource(fn)
 for old,new in replacements:check(source.count(old)==1,'private lifecycle anchor changed');source=source.replace(old,new)
 ns=dict(scope);exec(compile(textwrap.dedent(source),str(__file__)+'[private lifecycle]','exec'),ns);return ns[fn.__name__]
admission=adapt(h.admission,[('root-admitted-conversation-object-session','root-admitted-conversation-turn-failure'),('conversation_boundaries_authorized','conversation_turn_failure_authorized'),('ports=list(PORTS)','ports=list(ALL_PORTS)')],dict(h.__dict__,ALL_PORTS=ALL_PORTS));h.admission=admission
_start=adapt(base.Chain.start,[("OPENAI_BASE_URL='http://127.0.0.1:9/v1'","OPENAI_BASE_URL=self.provider.base_url")],base.__dict__)
_cleanup=adapt(base.Chain.cleanup,[('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')],base.__dict__)
def doctor(root):
 check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released lifecycle drift')
 for p in ALL_PORTS:
  with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',p))!=0,'own port occupied')
 return h.doctor(root)
def remaining(deadline):
 n=deadline-time.monotonic();check(n>0,'single cleanup budget exhausted');return n
class Provider:
 """Invocation-held bounded server. Never persist headers or request bodies."""
 def __init__(self,owner):
  self.lock=threading.Lock();self.records=[];self.errors=[];self.mode='failed';self.workers=[];self.sockets=[];self.thread=None;self.server=None;self.base_url='http://127.0.0.1:'+str(PROVIDER_PORT)+'/v1';owner.provider=self;provider=self
  class Handler(BaseHTTPRequestHandler):
   def log_message(self,*a):pass
   def do_POST(self):
    reason='fixture_validation';status=None;raw=b''
    try:
     length=int(self.headers.get('Content-Length','-1'));check(self.path=='/v1/chat/completions' and self.headers.get('Authorization')=='Bearer synthetic-no-inference' and 0<length<=65536,'exact bounded own protocol required');raw=self.rfile.read(length);check(len(raw)==length,'partial protocol request');value=json.loads(raw);check(value.get('model')=='readonly-synthetic' and value.get('stream') is True,'owned streaming model required')
     with provider.lock:mode=provider.mode
     status=500 if mode=='failed' else 200;answer=provider_response(status);self.send_response(status);self.send_header('Content-Type','application/json' if status==500 else 'text/event-stream');self.send_header('Content-Length',str(len(answer)));self.end_headers();reason='fixture_write';self.wfile.write(answer);self.wfile.flush();reason='completed'
    except (OSError,ValueError,AssertionError):pass
    finally:
     with provider.lock:
      provider.records.append(dict(status=status,reason=reason,request_bytes=len(raw),request_sha256=hashlib.sha256(raw).hexdigest()))
      if reason!='completed':provider.errors.append(reason)
  class HTTP(ThreadingHTTPServer):
   block_on_close=False
   def process_request(self,request,address):
    request.settimeout(6)
    def work():
     try:self.finish_request(request,address)
     finally:self.shutdown_request(request)
    t=threading.Thread(target=work,daemon=True)
    with provider.lock:provider.sockets.append(request);provider.workers.append(t)
    t.start()
  self.server=HTTP(('127.0.0.1',PROVIDER_PORT),Handler);self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
 def snapshot(self):
  with self.lock:return dict(records=list(self.records),errors=list(self.errors))
 def recover(self):
  with self.lock:check(len(self.records)==1 and self.records[0]['status']==500 and not self.errors,'first provider terminal differs');self.mode='completed'
 def close(self,deadline):
  if self.server is None:return
  stopper=threading.Thread(target=self.server.shutdown,daemon=True);stopper.start();stopper.join(remaining(deadline));self.server.server_close()
  with self.lock:sockets=list(self.sockets);workers=list(self.workers)
  for sock in sockets:
   try:sock.shutdown(socket.SHUT_RDWR)
   except OSError:pass
   sock.close()
  if self.thread:self.thread.join(remaining(deadline))
  for t in workers:t.join(remaining(deadline))
  check(not stopper.is_alive() and (self.thread is None or not self.thread.is_alive()) and not any(t.is_alive() for t in workers),'owned provider worker cleanup incomplete')
def provider_response(status):
 if status==500:return b'{"error":{"message":"owned controlled provider failure"}}'
 check(status==200,'only reviewed provider modes');return ('data: '+json.dumps({'choices':[{'delta':{'content':ANSWER},'finish_reason':None}]})+'\n\n'+'data: '+json.dumps({'choices':[{'delta':{},'finish_reason':'stop'}]})+'\n\n'+'data: [DONE]\n\n').encode()
def accepted(value,text):
 check(isinstance(value,dict) and set(value)=={'message','reply_id'} and re.fullmatch('[a-f0-9]{32}',value['reply_id']),'accepted reply ID differs');m=value['message'];check(m.get('role')=='user' and re.fullmatch('[a-f0-9]{32}',m.get('id','')) and m.get('parts')==[dict(type='text',markdown=text)],'accepted user message differs')
def reply_events(ledger,rid):return sorted((r for r in ledger['v3_conversation_events'] if r['reply_id']==rid),key=lambda r:r['seq'])
def verify_ledger(created,after,cid,actor,turns):
 check(created['business']==after['business'] and after['v3_metrics_questions']==[],'ordinary turns changed unrelated public/source tables');convs=after['v3_conversations'];check(len(convs)==1 and convs[0]['id']==cid and convs[0]['owner_id']==actor and convs[0]['environment_id']=='test' and convs[0]['active_reply_id'] is None and convs[0]['metrics_source_required'] is False,'terminal conversation binding/active state differs')
 check({k:v for k,v in convs[0].items() if k not in ('updated_at','active_reply_id')}=={k:v for k,v in created['v3_conversations'][0].items() if k not in ('updated_at','active_reply_id')},'terminal changed conversation configuration');replies=after['v3_conversation_replies'];messages=after['v3_conversation_messages'];events=after['v3_conversation_events'];check(len(replies)==len(turns) and len(messages)==2*len(turns) and len({m['id'] for m in messages})==len(messages),'reply/message cardinality or orphan differs');ids={m['id'] for m in messages};audits=after['management_audit'];old=created['management_audit'];check(audits[:len(old)]==old and len(audits)==len(old)+2*len(turns),'turn audit prefix/cardinality differs')
 for rid,uid,state,text in turns:
  r=next((r for r in replies if r['id']==rid),None);check(r is not None and r['conversation_id']==cid and r['user_message_id']==uid and r['assistant_message_id'] in ids and r['state']==state and r['model_id']==MODEL and r['reasoning']=='' and r['finished_at'] is not None and r['lease_expires_at'] is None and bool(r['error'])==(state=='failed'),'exact terminal reply linkage differs');u=next(m for m in messages if m['id']==uid);a=next(m for m in messages if m['id']==r['assistant_message_id']);check(u['conversation_id']==a['conversation_id']==cid and u['role']=='user' and u['parts']==[dict(type='text',markdown=text)] and a['role']=='assistant' and a['state']==state,'linked durable messages differ');check(a['parts']==([dict(type='text',markdown=''),dict(type='status',text='Model reply failed',detail='Retry this message after checking the model connection.')] if state=='failed' else [dict(type='text',markdown=ANSWER)]),'durable failed/completed parts differ')
  es=reply_events(after,rid);check(es and len({e['seq'] for e in es})==len(es) and all(e['conversation_id']==cid for e in es),'events missing/duplicated/foreign');types=[e['payload']['type'] for e in es];check(types[0]=='message.started' and types[-1]=='message.completed' and types.count('message.started')==types.count('message.completed')==1 and set(types)<={'message.started','part.added','text.delta','message.completed'},'exact frozen terminal event protocol differs');check(types==['message.started','part.added',('part.added' if state=='failed' else 'text.delta'),'message.completed'] and es[1]['payload']['part']==dict(type='text',markdown='') and (state!='failed' or es[2]['payload']['part']==a['parts'][-1]),'controlled protocol event parts differ');check(es[0]['payload']['message']['id']==a['id'] and es[-1]['payload']['message_id']==a['id'],'event assistant linkage differs');check(all(e['payload'].get('message_id',a['id'])==a['id'] for e in es),'orphan event message');check(''.join(e['payload']['delta'] for e in es if e['payload']['type']=='text.delta')==('' if state=='failed' else ANSWER),'stream/durable text mismatch')
  expected=[dict(actor_id=actor,action='v3.conversation.message',target=cid,outcome='accepted',changes=dict(reply_id=rid,model_id=MODEL,environment='test',reasoning='')),dict(actor_id=actor,actor_kind='system',action='v3.conversation.reply',target=rid,outcome=state,changes=dict(conversation_id=cid))]
  for wanted in expected:check(sum(all(row.get(k)==v for k,v in wanted.items()) for row in audits)==1,'exact accepted/terminal audit missing/duplicate')
 check({e['reply_id'] for e in events}=={r[0] for r in turns} and ids=={v for r in replies for v in (r['user_message_id'],r['assistant_message_id'])},'orphan reply/event/message remains')
def parse_sse(raw):
 check(len(raw)<=65536,'bounded retained SSE exceeded');out=[]
 for block in raw.decode().split('\n\n'):
  if not block or block.startswith(':'):continue
  lines=block.splitlines();check(len(lines)==2 and lines[0].startswith('id: ') and lines[1].startswith('data: '),'exact retained SSE framing differs');seq=int(lines[0][4:]);out.append(dict(seq=seq,payload=json.loads(lines[1][6:])))
 check([r['seq'] for r in out]==sorted({r['seq'] for r in out}),'SSE duplicate/unordered IDs');return out
def verify_replay(status,headers,raw,events,cursor):
 check(status==200 and h.header(headers,'Content-Type').split(';')[0]=='text/event-stream','SSE status/type differs');wanted=[dict(seq=e['seq'],payload=e['payload']) for e in events if e['seq']>cursor];check(parse_sse(raw)==wanted,'SSE cursor lost/repeated/changed retained event')
class Chain(h.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id,permit);self.users={'reader':self.users['reader']};self.roles={'reader':self.roles['reader']};self.provider=None;self.cleanup_deadline=None;self.state.update(suite=MODULE,ports=list(ALL_PORTS),normal_added=0,model_scope='controlled protocol Mock only');base._private(self.work/'state.json',self.state)
 def frozen(self):check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released lifecycle drift');super().frozen()
 def marker_case(self,name,value):
  if name=='identities':name='identity';value=dict(synthetic_owner_only=True,SQL_business_writes=0)
  self.frozen();h.emit('conversation_turns.'+name,'passed',dict(run_id=self.run_id,**value))
 def start(self):Provider(self);self.marker_case('provider',dict(loopback=PROVIDER_PORT,controlled_protocol_mock=True));_start(self)
 def terminal(self,cid,rid,state):
  deadline=time.monotonic()+30
  while time.monotonic()<deadline:
   value=json.loads(self.sql('SELECT json_build_object(\'state\',r.state,\'active\',c.active_reply_id) FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id WHERE r.id='+q(rid)+' AND c.id='+q(cid),read=True))
   if value['state'] in ('failed','completed'):check(value==dict(state=state,active=None),'wrong terminal/no release');return
   time.sleep(.1)
  raise Blocked('bounded durable terminal missing; no resend')
 def replay(self,cid,rid,ledger,index):
  events=reply_events(ledger,rid);check(len(events)>=3,'replay needs started/part/terminal');before=self.provider.snapshot()
  for cursor in (0,events[0]['seq'],events[-1]['seq']):
   self.frozen();self.calls+=1;path='/api/v1/conversations/'+cid+'/events';self.contract.canonical('GET',path);row=dict(name='conversation_turns.http.'+str(self.calls),method='GET',path='/api/v1/conversations/{id}/events',route_class='main',scenario='normal',expected=dict(status=[200],error_code=None),run_id=self.run_id,call_id=self.run_id+'-'+str(self.calls),registration_evidence=False)
   try:
    status,raw,headers=base.transport(base.BASES[index],'GET',path+'?reply_id='+rid,headers={'Origin':base.BASES[index],'Cookie':self.cookies['reader'],'Last-Event-ID':str(cursor)},timeout=12);row['actual']=dict(status=status,error_code=None);base._private(self.work/('sse-received-'+str(self.calls)+'.json'),dict(status=status,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),reply_id=rid,cursor=cursor,binary_sha256=BINARY,source_tree_sha=SOURCE));verify_replay(status,headers,raw,events,cursor);h.same(ledger,self.ledger());check(before==self.provider.snapshot(),'replay invoked model');base._private(self.work/('sse-wire-'+str(self.calls)+'.json'),dict(reply_id=rid,cursor=cursor,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),events=parse_sse(raw),source_tree_sha=SOURCE,binary_sha256=BINARY));row['status']='passed'
   except Exception as e:
    row.setdefault('actual',dict(status=None));row['status']='failed';row['actual']['assertion']=type(e).__name__;self.first_red(row);base.append_json(self.work/'interfaces.jsonl',row);h.emit(row['name'],'failed',row['actual'],**({'interface':row} if type(row['actual']['status']) is int else {}));raise AssertionError('retained SSE first failure; no retry') from None
   base.append_json(self.work/'interfaces.jsonl',row);h.emit(row['name'],'passed',row['actual'],interface=row)
 def run(self):
  self.provision();self.start();self.seed();self.login('reader');self.call('reader','GET','/api/v1/models',scenario='fixture',validate=lambda v:check(len(v['items'])==1 and v['items'][0]['id']==MODEL,'configured model differs'));before=self.ledger();check(all(before[t]==[] for t in TABLES),'fresh six business/audit tables required');title='Owned turn failure '+self.nonce;value,_=self.call('reader','POST','/api/v1/conversations',dict(text=title,context='',model=MODEL),want=201,scenario='fixture');cid=value['id'];created=self.ledger();h.verify_created(before,created,cid,self.users['reader'],title);base._private(self.work/'created-ledger.json',created);self.marker_case('created',dict(HTTP201=True,ordinary=True));turns=[];previous=created
  for index,state in enumerate(('failed','completed')):
   if index:self.provider.recover();self.marker_case('recovery',dict(explicit_provider_recovery=True,no_automatic_send_retry=True))
   text='Owned '+state+' turn '+self.nonce;v,_=self.call('reader','POST','/api/v1/conversations/'+cid+'/messages',dict(text=text,model=MODEL),want=202,index=index,validate=lambda v:accepted(v,text));rid=v['reply_id'];self.terminal(cid,rid,state);turns.append((rid,v['message']['id'],state,text));ledger=self.ledger();verify_ledger(created,ledger,cid,self.users['reader'],turns);check(all(row in ledger[t] for t in TABLES[1:] for row in previous[t]),'recovery changed prior immutable turn/event/audit rows');previous=ledger;base._private(self.work/(state+'-ledger.json'),ledger);self.marker_case(state,dict(accepted202=True,durable_state=state,active_reply_NULL=True));self.replay(cid,rid,ledger,1-index);self.marker_case(state+'_replay',dict(full_suffix_and_past_terminal=True,no_model_delta=True))
  details=[]
  for index in (0,1):
   v,_=self.call('reader','GET','/api/v1/conversations/'+cid,index=index,validate=lambda v:verify_detail(v,cid,title,ledger));details.append(v);h.same(ledger,self.ledger())
  check(details[0]==details[1],'cross instance terminal detail differs');self.marker_case('detail',dict(two_API_same=True,failed_and_completed_messages=4));snap=self.provider.snapshot();check(not snap['errors'] and [r['status'] for r in snap['records']]==[500,200],'provider received unexpected retry or failure');base._private(self.work/'provider-receipts.json',snap);self.marker_case('ledger',dict(no_orphan=True,unrelated_public_unchanged=True,provider_requests=2,real_model=False));check(self.calls==13,'reviewed13HTTP scope differs');result=dict(run_id=self.run_id,http_calls=self.calls,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY,upstream_mode='protocol_mock');base._private(self.work/'turn-result.json',result);return result
 def cleanup(self):
  self.cleanup_deadline=time.monotonic()+40;errors=[]
  if self.provider:
   try:self.provider.close(min(self.cleanup_deadline,time.monotonic()+4))
   except BaseException as e:errors.append(type(e).__name__)
  try:_cleanup(self)
  except BaseException as e:errors.append(type(e).__name__)
  absent={}
  for p in ALL_PORTS:
   remaining(self.cleanup_deadline)
   with socket.socket() as sock:sock.settimeout(min(.2,remaining(self.cleanup_deadline)));absent[str(p)]=sock.connect_ex(('127.0.0.1',p))!=0
  base._private(self.work/'turn-cleanup.json',dict(errors=errors,ports_absent=absent,single_seconds=40));check(not errors and all(absent.values()),'single40 cleanup incomplete');h.emit('conversation_turns.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True))
def verify_detail(value,cid,title,ledger):
 check(value.get('id')==cid and value.get('title')==title and value.get('scope')==dict(env='test',inferred=False) and value.get('status')=='active' and value.get('mode')=='chat' and value.get('artifacts')==[],'ordinary detail binding differs');ms=value.get('messages');rows=ledger['v3_conversation_messages'];check(isinstance(ms,list) and len(ms)==len(rows) and {m['id'] for m in ms}=={m['id'] for m in rows},'detail dropped/repeated durable message')
 for m in ms:
  row=next(r for r in rows if r['id']==m['id']);check(m['role']==row['role'] and m['parts']==row['parts'] and not m.get('streaming',False),'detail parts/state differ');check(row['role']!='assistant' or m.get('state')==row['state'],'detail lost failed/completed state')
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=ALL_PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=13,normal_added=0,registration_evidence=False,upstream_mode='protocol_mock',API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with base.exclusive(ROOT),base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:h.emit('conversation_turns.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:h.emit('conversation_turns.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())

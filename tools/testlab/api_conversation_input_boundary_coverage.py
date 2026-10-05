"""Formal empty conversation Messages input refusals only; no inference.
Private reuse of released atomic UUID/Popen lifecycle, root live admission only.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,textwrap,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-conversation-message-input-boundaries';PORTS=(18868,18869)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb';BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70';RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d';PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
# Freeze the released corrected compiled transport seam, never copy its old bug.
BASE_HELPER_SHA='6501cb3c5e9603d8071731b963eb92e19c1a65d286326ecff8639e3bfd829d11'
spec=importlib.util.spec_from_file_location('input_boundary_private_conversation',ROOT/'tools/testlab/api_conversation_object_session_coverage.py');h=importlib.util.module_from_spec(spec);spec.loader.exec_module(h)
check,Blocked=h.check,h.Blocked;base=h.base;MODEL=h.MODEL;TARGETS=(('POST','/conversations/{id}/messages'),);SUPPORT=(('POST','/auth/login'),('GET','/models'),('POST','/conversations'),('GET','/conversations/{id}'))
TABLES=(*h.TABLES,'v3_metrics_questions');h.TABLES=TABLES;h.MODULE=MODULE;h.PORTS=PORTS;base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);h._namespace['BASES']=base.BASES
CASES=('blank_text','reasoning_length','unknown_model','unsupported_reasoning','missing_attachment','unknown_field','max_body')
REQUIRED_CASES=tuple('conversation_inputs.'+n for n in ('identity','configured_model','created','baseline',*CASES,'ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
MESSAGES={'invalid_text':'Enter a message under 2 MiB','invalid_reasoning':'Invalid reasoning choice','invalid_model':'Selected model is not configured','unsupported_reasoning':'Selected reasoning level is not supported by this model','invalid_attachment':'Attachment is unavailable or outside this environment','invalid_body':'Invalid JSON body'}
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_conversation_input_boundary_coverage.py','tests/test_api_conversation_input_boundary_coverage.py','api_conversation_object_session_coverage.py','tests/test_api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def verify_error(value,headers,status,code):
 check(status==422 and code in MESSAGES,'only reviewed input422 permitted');check(h.header(headers,'Content-Type').split(';',1)[0].strip().lower()=='application/json','input refusal must be JSON');check(value=={'error':{'code':code,'message':MESSAGES[code]}},'exact sanitized input refusal differs or reflects input')
h._namespace['verify_error']=verify_error
# Private callbacks keep raw and normalized HTTP evidence explicitly unqualified.
_original_evidence=h.evidence
def evidence(row):return dict(_original_evidence(row),registration_evidence=False,test_name='FormalConversationInputBoundaries',proof_scope='formal empty same-owner conversation input refusal and unchanged ledger; no model call, active SSE, producer or whole acceptance')
h.evidence=evidence
class Contract(h.Contract):
 def canonical(self,method,path):
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside message input scope');return canonical,kind
base.Contract=Contract
_admission_source=inspect.getsource(h.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-conversation-input-boundary'),('conversation_boundaries_authorized','conversation_input_boundaries_authorized')):
 check(_admission_source.count(old)==1,'private admission anchor changed');_admission_source=_admission_source.replace(old,new)
_scope=dict(h.__dict__);exec(compile(textwrap.dedent(_admission_source),str(__file__)+'[private admission]','exec'),_scope);admission=_scope['admission'];h.admission=admission
def doctor(root):
 check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released lifecycle drift');return h.doctor(root)
def fixtures(nonce):
 missing=uuid.uuid5(uuid.UUID(hex=nonce),'missing-input-upload').hex;valid=dict(text='Owned input sentinel '+nonce,model=MODEL)
 body=dict(valid);body['text']='Owned max-body sentinel '+nonce;overhead=len(json.dumps(body).encode());body['text']+='x'*((2<<20)+1-overhead);check(len(json.dumps(body).encode())==(2<<20)+1,'wire max-body fixture must exceed reader limit by exactly one byte')
 return (
  ('blank_text',dict(valid,text=' \t\n '),'invalid_text'),
  ('reasoning_length',dict(valid,reasoning='r'*129),'invalid_reasoning'),
  ('unknown_model',dict(valid,model='own-unconfigured-'+nonce),'invalid_model'),
  ('unsupported_reasoning',dict(valid,reasoning='high'),'unsupported_reasoning'),
  ('missing_attachment',dict(valid,attachments=[dict(id=missing,kind='paste',name='paste-1',size=1,mime='text/plain')]),'invalid_attachment'),
  ('unknown_field',dict(valid,unknown_owned_field=nonce),'invalid_body'),
  ('max_body',body,'invalid_body'))
def verify_models(value):
 check(isinstance(value,dict) and isinstance(value.get('items'),list) and len(value['items'])==1 and value['items'][0].get('id')==MODEL and value['items'][0].get('reasoning')==dict(kind='none',levels=[]),'advertised inert model/reasoning fixture differs')
class Chain(h.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id,permit);self.users={'reader':self.users['reader']};self.roles={'reader':self.roles['reader']};self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0);base._private(self.work/'state.json',self.state)
 def marker_case(self,name,value):
  if name=='identities':name='identity';value=dict(synthetic_owner_only=True,SQL_business_writes=0,source='exact scoped own identity/ACL')
  self.frozen();h.emit('conversation_inputs.'+name,'passed',dict(run_id=self.run_id,**value))
 def frozen(self):
  check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released lifecycle changed');super().frozen()
 def call(self,*a,**kw):
  original=base.transport;cloned=h._namespace['transport']
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 contract changed');result=original(origin,method,path,payload,headers,timeout)
   if '/conversations/' in path:
    status,raw,_=result;check(len(raw)<=4096,'bounded safe input response exceeded');base._private(self.work/('input-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,API_index=base.BASES.index(origin),status=status,request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),response_body=raw.decode('utf-8','replace'),binary_sha256=BINARY,source_tree_sha=SOURCE,request_body_stored=False))
   return result
  base.transport=wire;h._namespace['transport']=wire
  try:return super().call(*a,**kw)
  finally:base.transport=original;h._namespace['transport']=cloned
 def exercise(self,cid,title,baseline,detail):
  samples=fixtures(self.nonce)
  missing=samples[4][1]['attachments'][0]['id'];check(self.sql('SELECT EXISTS(SELECT 1 FROM v3_uploads WHERE id='+h.q(missing)+')',read=True)=='f','missing upload fixture unexpectedly exists')
  for index,(name,body,code) in enumerate(samples):
   before=self.ledger();h.same(baseline,before);self.call('reader','POST','/api/v1/conversations/'+cid+'/messages',body,want=422,code=code,scenario='boundary',index=index%2);h.same(baseline,self.ledger());self.marker_case(name,dict(status=422,error_code=code,API_index=index%2,no_turn_audit_delta=True,request_bytes=len(json.dumps(body).encode()),request_body_stored=False))
  value,_=self.call('reader','GET','/api/v1/conversations/'+cid,index=1,validate=lambda v:h.verify_detail(v,cid,title));check(value==detail,'old empty DTO changed after input refusals');h.same(baseline,self.ledger())
 def run(self):
  self.provision();self.start();self.seed();self.login('reader');models,_=self.call('reader','GET','/api/v1/models',scenario='fixture',validate=verify_models);self.marker_case('configured_model',dict(id=MODEL,reasoning_none=True,inference_called=False))
  before=self.ledger();check(all(before[t]==[] for t in TABLES),'fresh own six business/audit tables not empty');title='Owned input conversation '+self.nonce;value,_=self.call('reader','POST','/api/v1/conversations',dict(text=title,context='',model=MODEL),want=201,scenario='fixture');check(isinstance(value,dict) and set(value)=={'id'} and re.fullmatch('[a-f0-9]{32}',value['id']),'formal create ID differs');cid=value['id'];baseline=self.ledger();h.verify_created(before,baseline,cid,self.users['reader'],title);check(baseline['v3_metrics_questions']==[],'ordinary create fabricated metrics source');base._private(self.work/'created-ledger.json',baseline);self.marker_case('created',dict(HTTP201=True,one_audit=True,no_turn=True))
  detail,_=self.call('reader','GET','/api/v1/conversations/'+cid,validate=lambda v:h.verify_detail(v,cid,title));h.same(baseline,self.ledger());self.marker_case('baseline',dict(empty_detail200=True));self.exercise(cid,title,baseline,detail);after=self.ledger();h.same(baseline,after);base._private(self.work/'final-ledger.json',after);self.marker_case('ledger',dict(six_business_audit_exact=True,all_other_public_digests_exact=True,model_calls=0));check(self.calls==12,'reviewed exact12 HTTP scope differs')
  result=dict(run_id=self.run_id,http_calls=self.calls,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY,model_calls=0);base._private(self.work/'input-boundaries-result.json',result);return result
 def cleanup(self):
  # The original atomic cleanup owns the sole40 clock; no faults/restore added.
  base.Chain.cleanup(self)
  for port in PORTS:
   with h.socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own input API port remains')
  h.emit('conversation_inputs.cleanup','passed',dict(single_seconds=40,grace_seconds=45,owned_UUID_only=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,cases=CASES,required_cases=REQUIRED_CASES,estimated_HTTP_calls=12,normal_added=0,registration_evidence=False,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with base.exclusive(ROOT),base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:h.emit('conversation_inputs.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:h.emit('conversation_inputs.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())

"""Three precise upload input refusals and non-deduplicating paste repeat201.
One owned UUID DB/two APIs; bytea/audit readback plus all-public hashes and TMPDIR.
Paste has no download URL by contract: bytes are verified by owned SQL, not GET200.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-upload-input-repeat-boundaries';PORTS=(18930,18931)
def load(name,path):
 s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
h=load('upload_inputs_private_upload',ROOT/'tools/testlab/api_upload_audit_rollback_coverage.py');gate=load('upload_inputs_private_gate',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
ro=h.ro;check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=h.SOURCE_SHA,h.BINARY_SHA,h.RUNTIME_SHA,h.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('POST','/uploads'),('POST','/conversations'),('POST','/alert-rules/preview'));SUPPORT=(('POST','/auth/login'),('POST','/services'))
CASES=('metadata_only_file','declared_size_mismatch','unknown_JSON_field','conversation_blank','conversation_unknown_field','rule_op','rule_for','rule_range')
REQUIRED_CASES=tuple('upload_input.'+n for n in ('http_inputs','empty',*CASES,'first_paste','repeated_paste','ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
ERROR={'error':{'code':'invalid_upload','message':'Upload is too large or has an unsupported format'}}
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
 return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_upload_input_repeat_boundaries.py','tests/test_api_upload_input_repeat_boundaries.py','api_upload_audit_rollback_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalUploadInputRepeatBoundaries',proof_scope='eight JSON422 input refusals and identical paste repeat with two fresh IDs/two bytea/two audit; no download or whole acceptance')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(h.Contract.__bases__[0]):
 def canonical(self,method,path):
  parsed=h.urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact upload input path required')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'only upload input/login scope, paste has no content download');return canonical,kind
h.Contract=Contract;ro.Contract=Contract
_src=inspect.getsource(gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-upload-input-repeat-boundaries'),('conversation_boundaries_authorized','upload_input_repeat_authorized')):
 check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
def doctor(root):
 data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift')
 info=ro.inspect_pg(data['postgres_container']);check(info['Id']==PG_CID and info['HostConfig'].get('NanoCpus')==4000000000 and info['HostConfig'].get('Memory')==2147483648,'exact PG4CPU2GiB required')
 data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());h.frozen_identity(data,info['Id']);return data

def samples(nonce,app='own-app'):
 text='Owned paste repeat 中文 '+nonce+'\n';valid=dict(kind='paste',name='owned-paste-'+nonce,text=text,size=len(text.encode()),mime='text/plain')
 reject=[('metadata_only_file','/uploads',dict(kind='file',name='owned-file-'+nonce+'.txt',size=1,mime='text/plain'),'invalid_upload',ERROR['error']['message']),('declared_size_mismatch','/uploads',dict(valid,size=valid['size']+1),'invalid_upload',ERROR['error']['message']),('unknown_JSON_field','/uploads',dict(valid,unknown_owned_field=nonce),'invalid_upload',ERROR['error']['message'])]
 reject.extend([('conversation_blank','/conversations',dict(text=' ',context='',model='openai:readonly-synthetic'),'invalid_body','Enter a prompt within the conversation input limits'),('conversation_unknown_field','/conversations',dict(text='Owned rejected conversation '+nonce,context='',model='openai:readonly-synthetic',unexpected=True),'invalid_body','Invalid JSON body')])
 rule=dict(query='up',application_id=app,op='>',threshold=1,for_minutes=0,range='1h')
 for name,field,bad in (('rule_op','op','=='),('rule_for','for_minutes',61),('rule_range','range','8d')):reject.append((name,'/alert-rules/preview',dict(rule,**{field:bad}),'invalid_rule_preview','Query, condition or range is invalid'))
 return valid,tuple(reject)
def verify_error(value,headers):
 check(value==ERROR and next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','exact sanitized invalid_upload422 required')
def verify_paste(value,body):
 check(isinstance(value,dict) and set(value)=={'id','kind','name','size','mime'} and re.fullmatch('[a-f0-9]{32}',value['id']) and all(value[k]==body[k] for k in ('kind','name','size','mime')),'exact paste DTO without text/URL required')
def verify_rows(before,after,ids,actor,body):
 check(before['uploads']==[] and before['audit']==[] and before['revocations']==[],'fresh own upload rows required')
 check(after['revocations']==[] and len(after['uploads'])==len(ids) and len(after['audit'])==len(ids) and len(set(ids))==len(ids),'one distinct row/audit per accepted paste required')
 expected=[dict(id=uid,owner_id=actor,environment_id='test',kind='paste',name=body['name'],size=body['size'],mime='text/plain',url='',bytes=body['size'],sha256=hashlib.sha256(body['text'].encode()).hexdigest()) for uid in sorted(ids)]
 check(after['uploads']==expected,'exact owner/env/bytea/size/hash of repeated paste differs')
 check({a['target'] for a in after['audit']}==set(ids) and all(a==dict(actor_id=actor,target=a['target'],outcome='success',kind='paste',size=body['size'],mime='text/plain',environment_id='test') for a in after['audit']),'one exact transactional upload audit per new ID required')
 check(set(before['all_public'])==set(after['all_public']) and {t for t in before['all_public'] if before['all_public'][t]!=after['all_public'][t]}=={'v3_uploads','management_audit'},'accepted upload changed other public tables including sessions')

class Chain(h.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,no_fault_DDL=True);ro._private(self.work/'state.json',self.state)
 def frozen(self):
  admission(self.permit,self.run_id);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift during upload batch');super().frozen()
 def marker_case(self,name,value):
  check('upload_input.'+name in REQUIRED_CASES,'unknown upload input marker');self.frozen();emit('upload_input.'+name,'passed',dict(run_id=self.run_id,**value))
 def ledger(self):
  value=h.Chain.ledger(self)
  tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'",read=True))
  check(tables and len(tables)<=512 and {'sessions','management_audit','v3_uploads'}.issubset(tables) and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded all-public inventory required')
  parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+ro.identifier(t)+' t' for t in tables]
  value['all_public']=json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h',read=True))
  check(set(value['all_public'])==set(tables) and all(re.fullmatch('[a-f0-9]{64}',v) for v in value['all_public'].values()),'full public row digests required');return value
 def call(self,*args,**kwargs):
  kwargs.setdefault('scenario','boundary');original=ro.transport
  def wire(base,method,path,payload=None,headers=None,timeout=12):
   result=original(base,method,path,payload,headers,timeout)
   if (method,path.removeprefix('/api/v1')) in TARGETS:
    status,raw,hs=result;check(len(raw)<=4096,'bounded safe upload DTO evidence required');value=json.loads(raw)
    if status==422:
     expected=getattr(self,'expected_error',None);check(expected is not None and value==expected and next((v for k,v in hs.items() if k.lower()=='content-type'),'').split(';')[0]=='application/json','exact expected sanitized input422 required')
    ro._private(self.work/('upload-input-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,API_index=ro.BASES.index(base),status=status,response=value,response_sha256=hashlib.sha256(raw).hexdigest(),request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),request_body_stored=False,credentials_stored=False))
   return result
  ro.transport=wire
  try:return super().call(*args,**kwargs)
  finally:ro.transport=original
 def run(self):
  self.provision();self.start();self.login('admin')
  self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True);check(re.fullmatch('[a-f0-9]{32}',self.actor),'own bootstrapped admin identity required')
  key='upload-input-'+self.nonce;self.call('admin','POST','/api/v1/services',dict(items=[dict(id=key,tier='T1')]),want=201,scenario='fixture')
  self.app=self.sql('SELECT id FROM applications WHERE key='+q(key),read=True);check(re.fullmatch('[a-f0-9]{32}',self.app),'HTTP-produced own service required');self.marker_case('http_inputs',dict(login=1,service201=True,SQL_business_writes=0,no_extra_component=True))
  valid,rejected=samples(self.nonce,self.app);before=self.ledger();files=self.files();check(before['uploads']==[] and before['audit']==[] and before['revocations']==[] and files=={},'fresh uploads/TMPDIR required')
  ro._private(self.work/'ledger-before.json',before);self.marker_case('empty',dict(uploads=0,upload_audits=0,tmp_files=0,all_public_tables=len(before['all_public'])))
  for index,(name,path,body,code,message) in enumerate(rejected):
   self.expected_error=dict(error=dict(code=code,message=message));self.call('admin','POST','/api/v1'+path,body,want=422,code=code,index=index%2)
   self.marker_case(name,dict(HTTP422=True,error_code=code,exact_safe_error=True))
  check(self.ledger()==before and self.files()==files,'eight refusals retained public rows/audit/session/blob/tmp residue');ro._private(self.work/'ledger-after-refusals.json',before)
  ids=[];first=None
  for index,phase in enumerate(('first_paste','repeated_paste')):
   value,_=self.call('admin','POST','/api/v1/uploads',valid,want=201,index=index,scenario='idempotency',validate=lambda v:verify_paste(v,valid));ids.append(value['id']);after=self.ledger();verify_rows(before,after,ids,self.actor,valid)
   exact=json.loads(self.sql("SELECT json_build_object('count',count(*),'exact_bytes',bool_and(content=decode("+q(valid['text'].encode().hex())+",'hex'))) FROM v3_uploads WHERE id=ANY(ARRAY["+','.join(q(uid) for uid in ids)+'])',read=True));check(exact==dict(count=len(ids),exact_bytes=True),'each accepted paste bytea must equal exact UTF8 input')
   if first is not None:check(all(row in after['uploads'] for row in first['uploads']) and all(row in after['audit'] for row in first['audit']),'repeat changed first row/audit')
   check(self.files()==files,'paste bytea left TMPDIR residue');ro._private(self.work/('ledger-'+phase+'.json'),after);self.marker_case(phase,dict(HTTP201=True,distinct_ID=True,persisted_bytes=valid['size'],same_body=True,exact_uploads=len(ids),exact_upload_audits=len(ids),no_dedup_contract=True));first=after
  check(self.calls==12 and ids[0]!=ids[1],'exact12HTTP/new-ID repeat semantics differs');self.marker_case('ledger',dict(target_POSTs=10,login=1,service=1,sessions_and_other_public_unchanged=True,paste_download_not_supported=True,TMPDIR_unchanged=True))
  result=dict(run_id=self.run_id,http_calls=12,target_POSTs=10,normal_added=0,registration_evidence=False,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'upload-input-result.json',result);return result
 def cleanup(self):
  ro.Chain.cleanup(self)
  for port in PORTS:
   with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own upload input listener remains')
  emit('upload_input.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,no_fault_DDL=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,cases=CASES,required_cases=REQUIRED_CASES,http_calls=12,target_POSTs=10,paste_download_not_supported=True,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('upload_input.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('upload_input.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())

"""Formal conversation object/session entry refusals; no inference or active SSE.

Only root --live admission acquires own UUID PG/role and two Popen handles.
Conversation creation is formal HTTP; SQL writes only synthetic identities/ACL.
"""
from __future__ import annotations
import argparse,copy,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,time,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-conversation-object-session-boundaries';PORTS=(18856,18857)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
spec=importlib.util.spec_from_file_location('conversation_boundaries_private_readonly',ROOT/'tools/testlab/api_readonly_chain.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);check,Blocked,q=base.check,base.Blocked,base.quote
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/conversations/{id}'),('POST','/conversations/{id}/messages'),('GET','/conversations/{id}/events'))
SUPPORT=(('POST','/conversations'),('GET','/models'),('POST','/auth/login'),('POST','/auth/logout'))
MODEL='openai:readonly-synthetic'
TABLES=('v3_conversations','v3_conversation_messages','v3_conversation_replies','v3_conversation_events','management_audit')
REQUIRED_CASES=tuple('conversation_boundaries.'+n for n in ('identities','configured_model','created','owner_detail','nonowner_messages','nonowner_events','crossenv_messages','logout','old_cookie_targets','cross_instance_logout','relogin_detail','ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_conversation_object_session_coverage.py','tests/test_api_conversation_object_session_coverage.py',
        'api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',
    source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalConversationObjectSession',
    proof_scope='formally-created empty conversation owner/env/session entry refusals only; no inference, active SSE, metrics producer or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=base.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=emit;base.append_json=append_json
_inspect=base.inspect_pg
def inspect_pg(*args,**kwargs):
    value=_inspect(*args,**kwargs);check(value.get('Id')==PG_CID,'exact ownedPG differs');return value
base.inspect_pg=inspect_pg
def frozen_identity(data):
    check(data.get('source_tree_sha')==SOURCE and data.get('binary_sha256')==BINARY and data.get('runtime_sha')==RUNTIME
        and data.get('helper_sha256')==hashes(),'source/binary/runtime/transitive helpers differ')
def doctor(root):
    check(Path(root).resolve()==ROOT,'reviewed checkout required');data=base.doctor(root)
    check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());frozen_identity(data);return data
def admission(path,run_id):
    path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonicalUUID/private admission required')
    st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 admission required')
    permit=base.private_json(path)
    expected=dict(status='root-admitted-conversation-object-session',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,
        binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'conversation root admission identity differs')
    for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','conversation_boundaries_authorized'):check(permit.get(key) is True,'missing root prerequisite '+key)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(row[0]) for row in conn.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required');return permit
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative conversation/auth/model path required')
        canonical,kind=super().canonical(method,path)
        operation=(method,canonical[len('/api/v1'):])
        check(operation in TARGETS+SUPPORT and (kind=='main' or operation in (('POST','/auth/login'),('POST','/auth/logout'))),
            'only reviewed conversation entry/auth/model scope permitted')
        return canonical,kind
base.Contract=Contract
def header(headers,name):return next((value for key,value in headers.items() if key.lower()==name.lower()),'')
def verify_error(value,headers,status,code):
    check(status in (401,404) and code in ('unauthorized','not_found'),'only declared entry refusals permitted')
    check(header(headers,'Content-Type').split(';',1)[0].strip().lower()=='application/json','refused SSE/entry must return JSON, never an event stream')
    check(isinstance(value,dict) and set(value)=={'error'} and isinstance(value['error'],dict) and set(value['error'])=={'code','message'}
        and value['error']['code']==code and isinstance(value['error']['message'],str) and 0<len(value['error']['message'])<=256,'exact sanitized refusal DTO differs')
    if status==404:check(value['error']['message']=='Conversation not found','object refusal leaked another detail')
# Extend the privately loaded call so error DTO validation happens before its
# immutable HTTP result is emitted, preserving the existing first-red path.
_call_source=inspect.getsource(base.Chain.call);needle='            if 200 <= status < 300:'
check(_call_source.count(needle)==1,'readonly call validation seam changed')
_namespace=dict(base.__dict__,verify_error=verify_error)
import textwrap
exec(compile(textwrap.dedent(_call_source.replace(needle,'            if code is not None:verify_error(value,response_headers,want,code)\n'+needle)),str(Path(__file__))+'[private refusal validation]','exec'),_namespace)
base.Chain.call=_namespace['call']
def same(before,after):check(before==after,'refused/read-only call changed full conversation/messages/replies/events/audit or other-public ledger')
def verify_detail(value,cid,title):
    check(isinstance(value,dict) and set(value)=={'id','title','mode','status','scope','messages','artifacts','side'},'exact empty conversation DTO fields differ')
    check(value['id']==cid and value['title']==title and value['mode']=='chat' and value['status']=='active'
        and value['scope']=={'env':'test','inferred':False} and value['messages']==[] and value['artifacts']==[],'owned empty conversation body differs')
    side=value['side'];check(isinstance(side,dict) and set(side)=={'connectors','context','recalled'} and side['connectors']==[] and side['recalled']==[],'empty conversation side differs')
    context=side['context'];check(isinstance(context,dict) and set(context)=={'used','budget','parts'} and type(context['used']) is int and context['used']==0
        and context['parts']==[] and (context['budget'] is None or type(context['budget']) is int and context['budget']>0),'empty context/unknown-or-configured budget differs')
def verify_created(before,after,cid,actor,title):
    check(before['business']==after['business'] and all(before[t]==after[t]==[] for t in TABLES[1:4]),'create changed unrelated business or started a turn')
    check(before[TABLES[0]]==[] and len(after[TABLES[0]])==1,'exact one formally created conversation required')
    row=after[TABLES[0]][0]
    check(row['id']==cid and row['owner_id']==actor and row['environment_id']=='test' and row['title']==title and row['context_ref']==''
        and row['default_model_id']==MODEL and row['status']=='active' and row['active_reply_id'] is None and row['metrics_source_required'] is False,'formal created row ownership/model/state differs')
    old=before['management_audit'];new=after['management_audit'];check(new[:len(old)]==old and len(new)==len(old)+1,'create must append exactly one audit')
    audit=new[-1];check(audit['actor_id']==actor and audit['application_id']=='' and audit['action']=='v3.conversation.create' and audit['target']==cid
        and audit['outcome']=='completed' and audit['changes']=={'environment':'test'},'exact conversation create audit differs')
class Chain(base.Chain):
    def __init__(self,root,data,run_id,permit):
        frozen_identity(data);super().__init__(root,data,run_id);self.permit=Path(permit)
        self.users={k:v for k,v in self.users.items() if k in ('reader','peer')};self.roles={k:self.roles[k] for k in self.users}
    def frozen(self):frozen_identity(self.data);admission(self.permit,self.run_id);super().frozen()
    def marker_case(self,name,value):self.frozen();emit('conversation_boundaries.'+name,'passed',dict(run_id=self.run_id,**value))
    def seed(self):
        statements=['BEGIN;'];expected=[]
        for name,actor in self.users.items():
            role=self.roles[name];email='member-'+self.nonce+'-'+name+'@testlab.invalid'
            statements.extend(['INSERT INTO users(id,email,name,role,password_hash) SELECT '+','.join(q(v) for v in (actor,email,self.marker+' '+name))+",'viewer',password_hash FROM users WHERE email="+q(self.admin_email)+';',
                'DELETE FROM rbac_user_roles WHERE user_id='+q(actor)+';','INSERT INTO rbac_roles(id,name,v3_kind) VALUES('+q(role)+','+q(role)+",'business');",'INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+q(actor)+','+q(role)+');'])
            for env in (('test','staging') if name=='reader' else ('test',)):
                for module,action in (('environment','view'),('conversation','view'),('conversation','change'),('models','view')):
                    expected.append(dict(role_id=role,module=module,action=action,scope_kind='environment',scope_id=env))
                    statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (role,module,action,'environment',env))+');')
        self.sql('\n'.join(statements+['COMMIT;']))
        actual=json.loads(self.sql('SELECT COALESCE(json_agg(json_build_object(\'role_id\',role_id,\'module\',module,\'action\',action,\'scope_kind\',scope_kind,\'scope_id\',scope_id)),\'[]\'::json) FROM v3_role_grants WHERE role_id=ANY(ARRAY['+','.join(q(v) for v in self.roles.values())+'])',read=True))
        check(sorted(actual,key=lambda v:json.dumps(v,sort_keys=True))==sorted(expected,key=lambda v:json.dumps(v,sort_keys=True)),'exact own two-human scoped grants differ')
        base._private(self.work/'identity-proof.json',dict(actors=self.users,grants=actual));self.marker_case('identities',dict(two_own_humans=True,owner_test_and_staging=True,peer_test_view_and_change=True,SQL_business_writes=0))
    def owner(self):
        query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        value=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user))
        check(value==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'exact UUID database/role owner tags differ')
    def ledger(self):
        self.frozen();self.owner();excluded=','.join(q(t) for t in (*TABLES,'sessions'))
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+excluded+')',read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        hashes=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        fields=["'business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(hashes)+') hashes)']+[q(t)+",(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM public."+t+' t)' for t in TABLES]
        return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')',read=True))
    def sessions(self):
        return json.loads(self.sql("SELECT json_build_object('reader',(SELECT count(*) FROM sessions WHERE user_id="+q(self.users['reader'])+"),'peer',(SELECT count(*) FROM sessions WHERE user_id="+q(self.users['peer'])+'))',read=True))
    def other_sessions(self):
        # A digest checks peer/admin/any other session without saving Cookie or
        # token_hash. Only this owner's logout/relogin may change the table.
        value=self.sql("SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(s) ORDER BY to_jsonb(s)::text),'[]'::jsonb)::text,'UTF8')),'hex') FROM sessions s WHERE user_id<>"+q(self.users['reader']),read=True)
        check(re.fullmatch('[a-f0-9]{64}',value),'other-session digest differs');return value
    def call(self,*args,**kwargs):
        self.frozen();original=base.transport;cloned_original=_namespace['transport']
        def wire(origin,method,path,payload=None,headers=None,timeout=12):
            check(timeout==12,'formal target API deadline changed');result=original(origin,method,path,payload,headers,timeout)
            if urlsplit(path).path.startswith('/api/v1/conversations') or path=='/api/v1/auth/logout':
                status,raw,_=result;base._private(self.work/('conversation-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,
                    API_index=base.BASES.index(origin),status=status,request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),
                    response_body=raw.decode('utf-8','replace'),source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
            return result
        base.transport=wire;_namespace['transport']=wire
        try:return super().call(*args,**kwargs)
        finally:base.transport=original;_namespace['transport']=cloned_original
    def refusal(self,actor,method,path,body=None,environment='test',status=404,code='not_found'):
        before=self.ledger();self.call(actor,method,path,body,index=1,environment=environment,want=status,code=code,scenario='authorization');same(before,self.ledger())
    def exercise(self,cid,title,baseline):
        detail,_=self.call('reader','GET','/api/v1/conversations/'+cid,validate=lambda v:verify_detail(v,cid,title));same(baseline,self.ledger())
        self.marker_case('owner_detail',dict(status=200,empty=True))
        body=dict(text='Owned rejected turn '+self.nonce,model=MODEL)
        self.refusal('peer','POST','/api/v1/conversations/'+cid+'/messages',body);self.marker_case('nonowner_messages',dict(status=404,same_env=True,valid_view_change_grants=True))
        self.refusal('peer','GET','/api/v1/conversations/'+cid+'/events');self.marker_case('nonowner_events',dict(status=404,JSON=True,persisted_owner_check=True,no_active_stream=True))
        self.refusal('reader','POST','/api/v1/conversations/'+cid+'/messages',body,environment='staging');self.marker_case('crossenv_messages',dict(status=404,same_owner_both_environments_authorized=True))
        check(self.sessions()=={'reader':1,'peer':1},'fresh exact owner/peer sessions required before logout')
        others=self.other_sessions();old=self.cookies['reader'];self.call('reader','POST','/api/v1/auth/logout',index=0,want=204,scenario='fixture')
        check(self.cookies['reader']==old and self.sessions()=={'reader':0,'peer':1},'logout must invalidate exactly the original owner session; retain old Cookie only in memory')
        check(self.other_sessions()==others,'logout changed another actor session')
        same(baseline,self.ledger());self.marker_case('logout',dict(status=204,owner_sessions=0,peer_sessions=1,old_cookie_retained_in_memory=True))
        for method,suffix,payload in (('GET','',None),('POST','/messages',body),('GET','/events',None)):
            check(self.cookies['reader']==old,'must send revoked Cookie, not anonymous replacement')
            self.refusal('reader',method,'/api/v1/conversations/'+cid+suffix,payload,status=401,code='unauthorized')
        self.marker_case('old_cookie_targets',dict(status=401,targets=3,other_API=True,no_turn_or_audit_delta=True))
        self.marker_case('cross_instance_logout',dict(logout_API_index=0,refused_API_index=1,targets=3,old_cookie_kept_in_memory=True,no_turn_or_audit_delta=True))
        self.login('reader');check(self.cookies['reader']!=old and self.sessions()=={'reader':1,'peer':1},'formal relogin must create one fresh owner session')
        check(self.other_sessions()==others,'relogin changed another actor session')
        recovered,_=self.call('reader','GET','/api/v1/conversations/'+cid,index=1,validate=lambda v:verify_detail(v,cid,title));check(recovered==detail,'same object detail changed after session recovery')
        same(baseline,self.ledger());self.marker_case('relogin_detail',dict(status=200,other_API=True,detail_unchanged=True))
    def run(self):
        self.provision();self.start();self.seed();self.login('reader');self.login('peer')
        models,_=self.call('reader','GET','/api/v1/models',scenario='fixture')
        check(isinstance(models,dict) and isinstance(models.get('items'),list) and len(models['items'])==1 and models['items'][0].get('id')==MODEL,'existing own inert configured model not advertised')
        self.marker_case('configured_model',dict(model=MODEL,configured_inert_only=True,no_probe_or_inference=True))
        before=self.ledger();check(all(before[t]==[] for t in TABLES),'fresh own conversation/event/audit tables must be empty')
        title='Owned conversation '+self.nonce
        value,_=self.call('reader','POST','/api/v1/conversations',dict(text=title,context='',model=MODEL),want=201,scenario='fixture')
        check(isinstance(value,dict) and set(value)=={'id'} and re.fullmatch('[a-f0-9]{32}',value['id']),'formal create id differs');cid=value['id']
        baseline=self.ledger();verify_created(before,baseline,cid,self.users['reader'],title)
        base._private(self.work/'created-ledger.json',baseline);self.marker_case('created',dict(status=201,conversation_id=cid,exact_one_audit=True,no_turn_started=True))
        self.exercise(cid,title,baseline);after=self.ledger();same(baseline,after);base._private(self.work/'final-ledger.json',after)
        self.marker_case('ledger',dict(full_conversation_messages_replies_events_audit_unchanged=True,all_other_public_digests_unchanged=True,sessions_mutations_checked_separately=True))
        check(self.calls==14,'reviewed exact14 HTTP scope differs');self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,required_cases=REQUIRED_CASES,http_calls=self.calls,
            normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,proof_scope='ordinary empty conversation object/session entry refusals only',
            unverified=['active SSE cutoff','metrics-backed app/source authorization','inference/Agent producer','natural session expiry','performance/24h/fullchain'])
        base._private(self.work/'conversation-boundaries-result.json',result);return result
    def cleanup(self):
        super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'owned conversation API port occupied after cleanup')
        emit('conversation_boundaries.cleanup','passed',dict(single_cleanup_seconds=40,grace_seconds=45,owned_UUID_only=True,no_fault_DDL=True))
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,support=SUPPORT,required_cases=REQUIRED_CASES,estimated_HTTP_calls=14,command_timeout_seconds=420,API_timeout_seconds=12,cleanup_seconds=40,cleanup_grace_seconds=45,normal_added=0,registration_evidence=False)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required')
    try:
        admission(args.admission,args.run_id)
        with base.exclusive(ROOT),base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked:emit('conversation_boundaries.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as error:emit('conversation_boundaries.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())

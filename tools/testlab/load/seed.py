"""Deterministic synthetic rows in the explicitly owned APP database only."""
from __future__ import annotations
import hashlib,json,os,re,secrets,subprocess
from pathlib import Path
from urllib.parse import urlparse
ROOT=Path(__file__).resolve().parents[3]
PROFILES={'check':(2,20,40),'base':(100,5000,10000),'scale':(1000,100000,500000)}

def private(path,data):
    path.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|getattr(os,'O_NOFOLLOW',0),0o600)
    with os.fdopen(fd,'w') as stream:json.dump(data,stream)
    os.chmod(path,0o600)

def fixture():
    path=ROOT/'.local/testlab/backend-fixture.json'
    if path.is_symlink() or path.stat().st_mode&0o777!=0o600:raise RuntimeError('private generated backend fixture required')
    data=json.loads(path.read_text()); env=json.loads((ROOT/'.local/testlab/environment.json').read_text())
    dsn=urlparse(data['database_url'])
    if data['owner']!=env['owner'] or data['postgres_container']!=env['container'] or dsn.hostname!='127.0.0.1' or dsn.path!='/aiops_testlab_app':raise RuntimeError('owned APP database identity mismatch')
    labels=json.loads(subprocess.check_output(['docker','inspect',data['postgres_container'],'--format','{{json .Config.Labels}}'],text=True))
    if labels.get('aiops.testlab.owner')!=data['owner']:raise RuntimeError('PostgreSQL owner label mismatch')
    if not re.fullmatch(r'testlab-[a-f0-9]{12}',data['owner']):raise RuntimeError('unexpected owner identifier')
    data['perf_work']=str(Path(env['work'])/'perf');Path(data['perf_work']).mkdir(mode=0o700,exist_ok=True)
    password_file=Path(data['perf_work'])/'users-password.json'
    if not password_file.exists():private(password_file,{'password':secrets.token_urlsafe(36)})
    if password_file.is_symlink() or password_file.stat().st_mode&0o777!=0o600:raise RuntimeError('private generated perf user password required')
    data['perf_password']=json.loads(password_file.read_text())['password']
    return data

def sql(data,statement):
    p=subprocess.run(['docker','exec','-i',data['postgres_container'],'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U','testlab','-d','aiops_testlab_app'],input=statement,text=True,capture_output=True,timeout=300)
    if p.returncode:
        error=Path(data['perf_work'])/'seed-error.log';error.write_text(p.stderr);os.chmod(error,0o600)
        raise RuntimeError('owned seed SQL failed; private seed-error.log retained')
    return p.stdout.strip()

def quote(value):return "'"+str(value).replace("'","''")+"'"

def seed(data,profile):
    env={k:v for k,v in os.environ.items() if k in ('PATH','HOME','USER','TMPDIR','GOPATH','GOCACHE','GOMODCACHE','GOPROXY','GOROOT','GOTOOLCHAIN')}
    env['GOCACHE']=str(ROOT/'.local/go-cache')
    hashed=subprocess.run(['go','run','-mod=readonly','./tools/testlab/load/hashpassword.go'],cwd=ROOT,input=data['perf_password'],text=True,capture_output=True,env=env,timeout=90)
    if hashed.returncode:raise RuntimeError('product password hashing helper unavailable')
    password_hash=quote(json.loads(hashed.stdout)['hash'])
    services,alerts,messages=PROFILES[profile];conversations=1000 if profile=='scale' else 200;prefix='perf-'+data['owner']+'-';environment=quote(data['environment']);admin=quote(data['admin']['email']);p=quote(prefix)
    # No delete/truncate: a larger seed remains visible and is reported as such.
    # Serializable transaction and count readback prevent pretending the requested scale was inserted.
    statement=f"""BEGIN; SET LOCAL statement_timeout='240s'; SELECT pg_advisory_xact_lock(20260930);
INSERT INTO users(id,email,name,role,password_hash)
 SELECT {p}||'user-'||g,{p}||'user-'||g||'@testlab.invalid','Performance user '||g,'operator',{password_hash}
 FROM generate_series(1,20) g ON CONFLICT(id) DO UPDATE SET password_hash=EXCLUDED.password_hash;
INSERT INTO rbac_roles(id,name) VALUES({p}||'role','Owned performance reader') ON CONFLICT(id) DO NOTHING;
INSERT INTO rbac_user_roles(user_id,role_id) SELECT {p}||'user-'||g,{p}||'role' FROM generate_series(1,20) g ON CONFLICT DO NOTHING;
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 SELECT {p}||'role',m,a,'environment',{environment} FROM unnest(ARRAY['environment','conversation','conversations','events','services','home','canvas']) m CROSS JOIN unnest(ARRAY['view','change']) a ON CONFLICT DO NOTHING;
INSERT INTO applications(id,name,key,environment)
 SELECT {p}||'app-'||g,'Performance service '||g,{p}||'app-'||g,{environment} FROM generate_series(1,{services}) g ON CONFLICT(id) DO NOTHING;
INSERT INTO application_environments(id,application_id,key,name)
 SELECT {p}||'env-'||g,{p}||'app-'||g,{environment},'Test' FROM generate_series(1,{services}) g ON CONFLICT(id) DO NOTHING;
INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT {p}||'app-'||g,{environment} FROM generate_series(1,{services}) g ON CONFLICT DO NOTHING;
INSERT INTO v3_service_environment_profiles(application_id,environment_id,tier,slo_target_percent,owner_label,p99_target_ms)
 SELECT {p}||'app-'||g,{environment},'T2',99.9,'Testlab',1000 FROM generate_series(1,{services}) g ON CONFLICT DO NOTHING;
INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at,labels)
 SELECT {p}||'alert-'||g,'testlab-performance',{p}||'app-'||(1+(g-1)%{services}),{p}||'env-'||(1+(g-1)%{services}),{p}||'fp-'||g,'Generated alert '||g,'warning','firing','2026-09-30T00:00:00Z'::timestamptz+g*interval '1 millisecond',jsonb_build_object('application',{p}||'app-'||(1+(g-1)%{services}),'environment',{environment},'service','synthetic-performance') FROM generate_series(1,{alerts}) g ON CONFLICT(id) DO UPDATE SET application_id=EXCLUDED.application_id,environment_id=EXCLUDED.environment_id,labels=EXCLUDED.labels;
INSERT INTO v3_conversations(id,owner_id,environment_id,title,default_model_id)
 SELECT {p}||'conv-'||g,{p}||'user-'||(1+(g-1)%20),{environment},'Generated conversation '||g,{quote(data['model_id'])} FROM generate_series(1,{conversations}) g ON CONFLICT(id) DO NOTHING;
INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
 SELECT {p}||'msg-'||g,{p}||'conv-'||(1+(g-1)%{conversations}),CASE WHEN g%2=0 THEN 'assistant' ELSE 'user' END,'Testlab',jsonb_build_array(jsonb_build_object('type','text','markdown','Synthetic performance message '||g)),'completed' FROM generate_series(1,{messages}) g ON CONFLICT(id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id;
COMMIT;
SELECT json_build_object('services',(SELECT count(*) FROM applications WHERE starts_with(id,{p}||'app-')),'alerts',(SELECT count(*) FROM ops_incidents WHERE starts_with(id,{p}||'alert-')),'messages',(SELECT count(*) FROM v3_conversation_messages WHERE starts_with(id,{p}||'msg-')),'users',(SELECT count(*) FROM users WHERE starts_with(id,{p}||'user-')));
"""
    counts=json.loads(sql(data,statement).splitlines()[-1]);wanted=dict(services=services,alerts=alerts,messages=messages,users=20)
    if any(counts[k]<v for k,v in wanted.items()):raise RuntimeError('seed count readback below requested profile')
    return {'profile':profile,'requested':wanted,'actual':counts,'prefix':prefix,'exact_profile':counts==wanted,'synthetic_business_data':True}

def stream_seed(data,users,run,count=60):
    prefix='perf-sse-'+run+'-';q=quote;rows=[]
    for i in range(count):
        user=users[i%len(users)];conv=prefix+str(i);reply=conv+'-reply';session=hashlib.sha256(user['cookie'].encode()).hexdigest()
        rows.extend([f"INSERT INTO v3_conversations(id,owner_id,environment_id,title,default_model_id) VALUES({q(conv)},{q(user['id'])},{q(data['environment'])},'Controlled SSE transport workload',{q(data['model_id'])});",f"INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES({q(conv+'-user')},{q(conv)},'user','Testlab','[]','completed'),({q(conv+'-assistant')},{q(conv)},'assistant','Testlab','[]','streaming');",f"INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,model_id,state,initiating_session_hash,worker_claim_id,lease_expires_at) VALUES({q(reply)},{q(conv)},{q(conv+'-user')},{q(conv+'-assistant')},{q(data['model_id'])},'running',{q(session)},{q(reply)},clock_timestamp()+interval '10 minutes');",f"UPDATE v3_conversations SET active_reply_id={q(reply)} WHERE id={q(conv)};"])
    sql(data,'BEGIN;'+''.join(rows)+'COMMIT;');return prefix

def stream_emit(data,prefix,large=False):
    p=quote(prefix);size=32768 if large else 256
    return sql(data,f"BEGIN; UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()+interval '10 minutes' WHERE starts_with(conversation_id,{p}) AND state='running'; INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) SELECT conversation_id,id,jsonb_build_object('type','text.delta','delta',repeat('x',{size})) FROM v3_conversation_replies WHERE starts_with(conversation_id,{p}) AND state='running'; COMMIT;")

def stream_finish(data,prefix):
    p=quote(prefix)
    sql(data,f"BEGIN; INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) SELECT conversation_id,id,'{{\"type\":\"message.completed\"}}'::jsonb FROM v3_conversation_replies WHERE starts_with(conversation_id,{p}) AND state='running'; UPDATE v3_conversation_replies SET state='completed',finished_at=clock_timestamp() WHERE starts_with(conversation_id,{p}); UPDATE v3_conversation_messages SET state='completed' WHERE starts_with(conversation_id,{p}); UPDATE v3_conversations SET active_reply_id=NULL WHERE starts_with(id,{p}); COMMIT;")

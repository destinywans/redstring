#!/usr/bin/env python3
"""Real backend smoke. Uses local .env; never prints passwords or session tokens.
--live-lab additionally injects a fault and executes the approved demo-only rollback.
--resume continues the prior committed branch after a service restart.
"""
import argparse, http.cookiejar, json, os, subprocess, sys, time, urllib.error, urllib.request, uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parent.parent
ap=argparse.ArgumentParser();ap.add_argument('--base',default='http://127.0.0.1:18080');ap.add_argument('--live-lab',action='store_true');ap.add_argument('--resume',action='store_true');args=ap.parse_args()
env={}
for line in (ROOT/'.env').read_text().splitlines():
    if line and not line.startswith('#') and '=' in line:
        k,v=line.split('=',1);env[k]=v
env.update(os.environ)
client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(method,path,body=None,expected=200):
    req=urllib.request.Request(args.base+path,data=None if body is None else json.dumps(body).encode(),method=method,headers={'Content-Type':'application/json'})
    try:
        with client.open(req,timeout=180) as r: status=r.status;data=r.read()
    except urllib.error.HTTPError as e: status=e.code;data=e.read()
    if status!=expected: raise RuntimeError(f'{method} {path}: expected {expected}, got {status}: {data[:300].decode()}')
    return json.loads(data) if data else None

def log(msg): print(msg,flush=True)
def await_run(inv,run_id):
    deadline=time.monotonic()+360;last=0
    while time.monotonic()<deadline:
        d=call('GET','/api/v1/investigations/'+inv)
        r=next(x for x in d['runs'] if x['id']==run_id)
        if r['status'] not in ('queued','running'):
            assert r['status']=='completed',r
            return d
        if time.monotonic()-last>20: log('Inference status: '+r['status']);last=time.monotonic()
        time.sleep(1)
    raise RuntimeError('Model run exceeded smoke deadline')

def ask(inv,branch,model,text):
    run=call('POST','/api/v1/branches/'+branch+'/messages',{'content':text,'model_id':model,'client_request_id':uuid.uuid4().hex},202)['run']
    return await_run(inv,run['id'])

call('GET','/healthz')
call('POST','/api/v1/auth/login',{'email':env['AIOPS_ADMIN_EMAIL'],'password':env['AIOPS_ADMIN_PASSWORD']})
statepath=ROOT/'.local/smoke-state.json'
if args.resume:
    state=json.loads(statepath.read_text())
    d=ask(state['investigation_id'],state['branch_id'],state['model_id'],'不要调用工具。请复述我最初提供的唯一核对标记，并根据之前已经保存的 Deployment 证据说出应用名。用一行回答。')
    b=next(x for x in d['branches'] if x['id']==state['branch_id'])
    assert state['marker'] in b['messages'][-1]['content'],b['messages'][-1]['content']
    log('PASS: process restart retained committed context and original marker.')
    raise SystemExit(0)

models=call('GET','/api/v1/models')['items'];assert models,'Configure a real model first'
model=next((m['id'] for m in models if m['provider']=='ollama'),models[0]['id'])
doc=call('POST','/api/v1/knowledge',{'application_id':'demo','title':'Demo Deployment rollback runbook','body':'When checkout returns 500 after a MODE=regression release, inspect the registered Deployment and its owned ReplicaSet history. Review a stable MODE=stable target. Create a specific rollback proposal, explicitly approve its digest, and execute. Verify replicas and healthy real requests continuously for 180 seconds. A ready pod alone is not proof of recovery.','source':'smoke://deployment-runbook','version':1})
assert doc['status']=='ready',doc
hits=call('POST','/api/v1/knowledge/search',{'application_id':'demo','query':'checkout rollback after release failure','limit':5})['items']
assert any(h['document_id']==doc['id'] for h in hits),'Imported source not retrievable'
log('PASS: real BGE-m3 import, pgvector retrieval and versioned citations.')
if args.live_lab:
    subprocess.run([str(ROOT/'scripts/lab-inject.sh')],cwd=ROOT,check=True)
inv=call('POST','/api/v1/investigations',{'application_id':'demo','title':'Backend acceptance '+time.strftime('%H:%M:%S')},201)
branch=inv['branches'][0]['id'];marker='CTX-'+uuid.uuid4().hex[:10]
state={'investigation_id':inv['id'],'branch_id':branch,'model_id':model,'marker':marker}
statepath.write_text(json.dumps(state));statepath.chmod(0o600)
d=ask(inv['id'],branch,model,f'本次核对标记是 {marker}。请先调用 inspect_deployment 取得当前应用的真实状态，再简要说明 Deployment 名称、业务是否健康和可能的发布异常。不要执行任何修改。')
evidence=[x for x in d['evidence'] if x['kind']=='kubernetes']
assert evidence,'Model did not collect real Deployment evidence'
log('PASS: real ADK tool call, committed evidence, streamed run completion.')
child=call('POST','/api/v1/branches/'+branch+'/fork',{'title':'Independent follow-up'},201)
parent=next(x for x in d['branches'] if x['id']==branch)
assert len(child['messages'])==len(parent['messages'])
call('PUT','/api/v1/investigations/'+inv['id']+'/layout',{'layout':{'selected_branch':child['id'],'cards':{branch:{'display':'minimized'}}}})
layout=call('GET','/api/v1/investigations/'+inv['id']+'/layout')
assert layout['layout']['selected_branch']==child['id']
log('PASS: branch inherited committed snapshot; layout persisted independently.')
if args.live_lab:
    content=json.loads(evidence[-1]['content'])
    revisions=content['snapshot']['revisions']
    stable=[]
    for rev in revisions:
        for container in rev['template']['spec']['containers']:
            if any(e.get('name')=='MODE' and e.get('value')=='stable' for e in container.get('env',[])): stable.append(rev['revision'])
    assert stable,'No stable owned revision available'
    target=max(stable,key=int)
    plan=call('POST','/api/v1/investigations/'+inv['id']+'/action-plans',{'target_revision':target})
    call('POST','/api/v1/action-plans/'+plan['id']+'/execute',expected=409)
    call('POST','/api/v1/action-plans/'+plan['id']+'/approve',{'digest':'wrong-digest'},409)
    call('POST','/api/v1/action-plans/'+plan['id']+'/approve',{'digest':plan['proposal']['digest']})
    execution=call('POST','/api/v1/action-plans/'+plan['id']+'/execute')
    again=call('POST','/api/v1/action-plans/'+plan['id']+'/execute')
    assert execution['id']==again['id']
    log('PASS: unapproved/wrong-digest rejected; duplicate execution request returned same operation.')
    deadline=time.monotonic()+360;last=''
    while time.monotonic()<deadline:
        execution=call('GET','/api/v1/executions/'+execution['id'])
        if execution['status']!=last:log('Execution status: '+execution['status']);last=execution['status']
        if execution['status'] in ('succeeded','failed','needs_attention'):break
        time.sleep(3)
    assert execution['status']=='succeeded',execution
    result=execution['result'];assert result['recovered'] and result['samples']>=36 and result['requests']>0,result
    state['execution_id']=execution['id'];state['recovery']=result;statepath.write_text(json.dumps(state,indent=2))
    log('PASS: approved real rollback + 180-second traffic/recovery window: '+json.dumps(result))
log('Saved smoke state for --resume; no secret credentials included.')

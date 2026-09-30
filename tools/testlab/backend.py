"""Explicit V3 binaries on an owned database; no business environment discovery."""
from __future__ import annotations
import argparse, hashlib, hmac, json, os, secrets, signal, socket, subprocess, sys, threading, time, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from env import _private, _cmd, prepare
from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.local/testlab/backend-fixture.json'
PORTS = (18561, 18562)
CONTROL = 18560

def result(name, status, actual='', **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module='formal-v3', evidence_mode='real_local', **fields),ensure_ascii=False),flush=True)

def get(url):
    with urllib.request.urlopen(url,timeout=3) as r: return json.load(r)

def identity():
    try:return get(f'http://127.0.0.1:{CONTROL}/identity')
    except (OSError,ValueError):return None

def stop(data):
    current = identity()
    if current is None:return
    if current.get('owner') != data['owner'] or current.get('pid') != data.get('supervisor_pid'):
        raise RuntimeError('V3 supervisor ownership mismatch; refusing signal')
    request=urllib.request.Request(f'http://127.0.0.1:{CONTROL}/shutdown',data=b'',method='POST',headers={'Authorization':'Bearer '+data['control_secret']})
    with urllib.request.urlopen(request,timeout=3) as response:
        if response.status!=202:raise RuntimeError('owned supervisor shutdown rejected')
    for _ in range(100):
        if identity() is None:return
        time.sleep(.1)
    raise RuntimeError('V3 supervisor did not stop')

def setup():
    environment=prepare(ROOT)
    fingerprint=source_fingerprint(ROOT)
    if fingerprint=='unknown':raise RuntimeError('backend source inputs cannot be verified; binary reuse/build is blocked')
    runtime_path=ROOT/'.local/testlab/runtime-config.json'
    runtime_sha=hashlib.sha256(runtime_path.read_bytes()).hexdigest() if runtime_path.exists() else None
    current=identity()
    old=None
    if STATE.exists():
        old=json.loads(STATE.read_text())
        if old.get('owner') != environment['owner'] or old.get('database_url') != environment['env']['TESTLAB_APP_DSN']:
            raise RuntimeError('backend fixture ownership/database mismatch')
        if current and current.get('ready') and current.get('owner')==environment['owner'] and current.get('source_tree_sha')==fingerprint and old.get('runtime_sha')==runtime_sha:
            return old
        if current:stop(old)
    for port in (CONTROL,*PORTS):
        with socket.socket() as s:
            if s.connect_ex(('127.0.0.1',port))==0:raise RuntimeError(f'owned V3 port {port} occupied')
    work=Path(environment['work'])/'backend';work.mkdir(mode=0o700,exist_ok=True)
    binary=work/'aiops'
    env=command_env({'GOCACHE':str(ROOT/'.local/go-cache')},{})
    build=subprocess.run(['go','build','-mod=readonly','-o',str(binary),'./cmd/aiops'],cwd=ROOT,env=env,text=True,capture_output=True,timeout=600)
    if build.returncode:raise RuntimeError('formal V3 build failed: '+build.stderr[-2000:])
    data={'owner':environment['owner'],'source_tree_sha':fingerprint,'runtime_sha':runtime_sha,'binary':str(binary),'work':str(work),
          'base_urls':[f'http://127.0.0.1:{port}' for port in PORTS],
          'database_url':environment['env']['TESTLAB_APP_DSN'],
          'migration_database_url':environment['env']['TEST_DATABASE_URL'],
          'postgres_container':environment['container'],
          'admin':old['admin'] if old else {'email':'admin@testlab.invalid','password':secrets.token_urlsafe(36)},
          'environment':'test','other_environment':'staging','model_id':'openai:testlab',
          'instance_config':str(Path(environment['work'])/'instance.json'),
          'evidence_mode_model':'protocol_mock','seed':20260930,'control_secret':secrets.token_hex(32)}
    _private(STATE,data)
    # Bootstrap intentionally leaves existing accounts unchanged. Reconcile only
    # this generated account in the owned test database; preserve its credential
    # across every subsequent source rebuild.
    seeded=subprocess.run(['go','run','-mod=readonly','./tools/testlab/credentials'],cwd=ROOT,env=env,
                          input=json.dumps({'database_url':data['database_url'],'admin':data['admin']}),text=True,capture_output=True,timeout=120)
    if seeded.returncode:raise RuntimeError('owned test administrator reconciliation failed')
    log=os.open(work/'supervisor.log',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
    proc=subprocess.Popen([sys.executable,str(Path(__file__).resolve()),'daemon'],cwd=ROOT,env=env,stdout=log,stderr=log,start_new_session=True)
    os.close(log);data['supervisor_pid']=proc.pid;_private(STATE,data)
    for _ in range(120):
        if proc.poll() is not None:raise RuntimeError('owned V3 supervisor exited; inspect .local/testlab backend logs')
        current=identity()
        if current and current.get('ready') and current.get('owner')==data['owner']:return data
        if current and current.get('failed'):raise RuntimeError('formal V3 binary startup failed; inspect owned backend logs')
        time.sleep(.5)
    raise RuntimeError('formal V3 startup timeout; no successful entry proof')

def daemon():
    data=json.loads(STATE.read_text());processes=[];stopping=threading.Event()
    extra_path=ROOT/'.local/testlab/runtime-config.json'
    extra=json.loads(extra_path.read_text()) if extra_path.exists() else {}
    if extra and extra.get('owner')!=data['owner']:raise RuntimeError('runtime owner mismatch')
    from runtime import start_proxies
    proxies=start_proxies(extra)
    for i,url in enumerate(data['base_urls']):
        variables={'DATABASE_URL':data['database_url'],'AIOPS_V3_INSTANCE_CONFIG':data['instance_config'],
                   'AIOPS_ADDR':f'127.0.0.1:{PORTS[i]}','AIOPS_PUBLIC_ORIGIN':url,'AIOPS_SECURE_COOKIES':'false',
                   'AIOPS_ADMIN_EMAIL':data['admin']['email'],'AIOPS_ADMIN_PASSWORD':data['admin']['password'],
                   'AIOPS_TRUSTED_PROXY_CIDRS':'127.0.0.1/32',
                   'OPENAI_MODEL':'testlab','OPENAI_BASE_URL':'http://127.0.0.1:18578/v1','OPENAI_API_KEY':'synthetic-testlab-key',
                   'EMBEDDING_PROVIDER':'openai','EMBEDDING_MODEL':'synthetic-testlab','EMBEDDING_BASE_URL':'http://127.0.0.1:18578/v1',
                   'EMBEDDING_API_KEY':'synthetic-testlab-key'}
        variables.update(extra.get('env',{}))
        env=command_env(variables,{})
        log=os.open(Path(data['work'])/f'api-{i+1}.log',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
        processes.append(subprocess.Popen([data['binary']],env=env,cwd=ROOT,stdout=log,stderr=log))
        os.close(log)
    def snapshot():
        alive=all(p.poll() is None for p in processes)
        ready=alive
        if ready:
            for url in data['base_urls']:
                try:get(url+'/healthz')
                except Exception:ready=False
        return {'owner':data['owner'],'pid':os.getpid(),'api_pids':[p.pid for p in processes],
                'source_tree_sha':data['source_tree_sha'],'ready':ready,'failed':not alive}
    class Identity(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def do_GET(self):
            if self.path!='/identity':self.send_error(404);return
            body=json.dumps(snapshot()).encode();self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        def do_POST(self):
            if self.path not in ('/shutdown','/fault'):self.send_error(404);return
            if not hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+data['control_secret']):self.send_error(403);return
            if self.path=='/fault':
                try:
                    size=int(self.headers.get('Content-Length','0'))
                    if not 0<size<=512 or self.headers.get('Transfer-Encoding'):raise ValueError()
                    operation=json.loads(self.rfile.read(size))
                    if set(operation)!={'owner','supervisor_pid','api_pid','signal'}:raise ValueError()
                    if operation['owner']!=data['owner'] or operation['supervisor_pid']!=os.getpid():raise ValueError()
                    target=next((p for p in processes if p.pid==operation['api_pid'] and p.poll() is None),None)
                    selected={'SIGTERM':signal.SIGTERM,'SIGKILL':signal.SIGKILL}.get(operation['signal'])
                    if target is None or selected is None:raise ValueError()
                except (ValueError,TypeError,KeyError):
                    self.send_error(409,'owned live API process required');return
                # This private controller owns these Popen objects. Register the
                # exact destructive target before sending either allowed signal.
                event=dict(operation,at=time.time())
                fd=os.open(Path(data['work'])/'fault-controls.jsonl',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
                try:
                    os.write(fd,(json.dumps(event)+'\n').encode());os.fsync(fd)
                finally:os.close(fd)
                try:target.send_signal(selected)
                except ProcessLookupError:self.send_error(409,'API process already exited');return
                body=json.dumps({'pid':target.pid,'signal':operation['signal']}).encode()
                self.send_response(202);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);return
            self.send_response(202);self.send_header('Content-Length','0');self.end_headers();stopping.set()
    server=ThreadingHTTPServer(('127.0.0.1',CONTROL),Identity)
    def halt(*args):stopping.set()
    signal.signal(signal.SIGTERM,halt);signal.signal(signal.SIGINT,halt)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    try:
        while not stopping.wait(.5):
            if any(p.poll() is not None for p in processes):break
    finally:
        for p in processes:
            if p.poll() is None:p.terminate()
        for p in processes:
            forced=False
            try:p.wait(timeout=20)
            except subprocess.TimeoutExpired:
                forced=True;p.kill();p.wait()
            fd=os.open(Path(data['work'])/'process-exits.jsonl',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
            try:
                os.write(fd,(json.dumps({'pid':p.pid,'returncode':p.returncode,'forced_kill':forced,'at':time.time()})+'\n').encode());os.fsync(fd)
            finally:os.close(fd)
        server.shutdown();server.server_close()
        for proxy in proxies:proxy.shutdown();proxy.server_close()

def main():
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','entry','daemon','stop']);a=p.parse_args()
    if a.command=='daemon':return daemon()
    if a.command=='stop':return stop(json.loads(STATE.read_text()))
    started=time.monotonic()
    try:
        data=setup()
        result('正式V3双二进制启动','passed',f'2 APIs; explicit instance; own app DB; seed={data["seed"]}',duration=time.monotonic()-started,
               expected='两个实例以显式V3配置完成真实数据库启动',gap_ids=[36,37,40])
        if a.command=='entry':
            if not (ROOT/'tests/backend').exists():
                result('正式入口黑盒用例','blocked','tests/backend尚未实现');return 2
            env=command_env({'GOCACHE':str(ROOT/'.local/go-cache'),'AIOPS_TESTLAB_BACKEND_ENABLED':'dedicated-temp-container',
                             'AIOPS_TESTLAB_BACKEND_FIXTURE':str(STATE)}, {})
            interface_log=ROOT/'.local/testlab/interface-results.jsonl'
            offset=interface_log.stat().st_size if interface_log.exists() else 0
            completed=subprocess.run(['go','test','-mod=readonly','-race','-count=1','-json','-timeout','15m','./tests/backend'],cwd=ROOT,env=env,text=True,capture_output=True)
            # Go JSON is converted to explicit results for this fixed protocol.
            outputs={}
            for line in completed.stdout.splitlines():
                try:event=json.loads(line)
                except ValueError:continue
                name=event.get('Test')
                if not name:continue
                if event['Action']=='output':outputs[name]=(outputs.get(name,'')+event.get('Output',''))[-8000:]
                elif event['Action'] in ('pass','fail','skip'):
                    result(name,{'pass':'passed','fail':'failed','skip':'skipped'}[event['Action']],outputs.get(name,''),duration=event.get('Elapsed',0),type='api')
            if interface_log.exists():
                from inventory import actual_interface
                with interface_log.open() as log:
                    log.seek(offset)
                    for line in log:
                        row=json.loads(line)
                        result(row['method']+' '+row['path']+' · '+row['scenario'],row['status'],
                               f"HTTP {row['actual']}; {row.get('test_name','')}",id=row['call_id'],type='api',
                               expected=f"HTTP {row['expected']}",interface=actual_interface(row))
            if completed.returncode:
                result('Go黑盒命令审计','failed',completed.stderr[-2000:] or 'one or more Go blackbox tests failed')
            return completed.returncode
        return 0
    except Exception as e:
        # Credentials remain in private files, never emitted in runner protocol.
        result('正式V3环境','blocked',type(e).__name__+': '+str(e));return 2

if __name__=='__main__':sys.exit(main() or 0)

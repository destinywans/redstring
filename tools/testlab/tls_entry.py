"""Bounded third real V3 process + dedicated TLS entry. Never alters base supervisor."""
from __future__ import annotations
import contextlib,http.client,json,os,ssl,subprocess,threading,time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from runner import command_env,source_fingerprint
from load.seed import fixture
from gitops import free_port,owned_container,ROOT

@contextlib.contextmanager
def entry(config):
    backend=fixture();runtime=json.loads((ROOT/'.local/testlab/runtime-config.json').read_text())
    if backend['owner']!=config['owner'] or runtime['owner']!=config['owner']:raise RuntimeError('third entry owner mismatch')
    if source_fingerprint(ROOT)!=backend['source_tree_sha']:raise RuntimeError('reuse binary requires exact current source')
    if not Path(backend['binary']).is_file():raise RuntimeError('verified binary unavailable')
    free_port(18583);free_port(18585)
    variables={'DATABASE_URL':backend['database_url'],'AIOPS_V3_INSTANCE_CONFIG':backend['instance_config'],'AIOPS_ADDR':'127.0.0.1:18585',
               'AIOPS_ADMIN_EMAIL':backend['admin']['email'],'AIOPS_ADMIN_PASSWORD':backend['admin']['password'],'AIOPS_TRUSTED_PROXY_CIDRS':'127.0.0.1/32',
               'OPENAI_MODEL':'testlab','OPENAI_BASE_URL':'http://127.0.0.1:18578/v1','OPENAI_API_KEY':'synthetic-testlab-key',
               'EMBEDDING_PROVIDER':'openai','EMBEDDING_MODEL':'synthetic-testlab','EMBEDDING_BASE_URL':'http://127.0.0.1:18578/v1','EMBEDDING_API_KEY':'synthetic-testlab-key'}
    variables.update(runtime.get('env',{}));variables.pop('AIOPS_V3_ALERT_RULE_TRIAL_CONFIG',None);variables.update(config['product_env'])
    log_path=Path(config['work'])/'third-api.log';fd=os.open(log_path,os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
    proc=subprocess.Popen([backend['binary']],cwd=ROOT,env=command_env(variables,{}),stdout=fd,stderr=fd);os.close(fd);server=None
    class Proxy(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def relay(self):
            if not (self.path.startswith('/api/v1/') or self.path in ('/healthz','/readyz')):self.send_error(404);return
            try:
                size=int(self.headers.get('Content-Length','0'))
                if not 0<=size<=1048576:self.send_error(413);return
                body=self.rfile.read(size) if size else None
                headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','connection','transfer-encoding','content-length','x-forwarded-for')};headers['X-Forwarded-For']='127.0.0.1'
                c=http.client.HTTPConnection('127.0.0.1',18585,timeout=60);c.request(self.command,self.path,body,headers);response=c.getresponse();raw=response.read(4<<20)
                self.send_response(response.status)
                for k,v in response.getheaders():
                    if k.lower() not in ('connection','transfer-encoding','content-length','server','date'):self.send_header(k,v)
                self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw);c.close()
            except (OSError,ValueError):self.send_error(502)
        do_GET=do_POST=do_PATCH=do_DELETE=do_PUT=relay
    try:
        deadline=time.monotonic()+60
        while time.monotonic()<deadline:
            if proc.poll() is not None:raise RuntimeError('third formal V3 startup failed; private third-api.log retained')
            try:
                c=http.client.HTTPConnection('127.0.0.1',18585,timeout=2);c.request('GET','/healthz');r=c.getresponse();r.read();c.close()
                if r.status==200:break
            except OSError:pass
            time.sleep(.5)
        else:raise RuntimeError('third formal V3 readiness timeout')
        server=ThreadingHTTPServer(('127.0.0.1',18583),Proxy);ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(config['third_cert'],config['third_key']);server.socket=ctx.wrap_socket(server.socket,server_side=True)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        yield backend
    finally:
        if server:server.shutdown();server.server_close()
        # Popen identity is held by this invocation; never kill an arbitrary saved PID.
        if proc.poll() is None:
            proc.terminate()
            try:proc.wait(timeout=20)
            except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=5)

"""Owned capture protocols and explicitly synthetic vendor fixtures.

Metrics, SMTP and Alertmanager delivery are measured local protocols. Vendor
responses are protocol_mock and never prove tenant permissions or paid models.
"""
from __future__ import annotations
import argparse, base64, hashlib, hmac, json, math, os, signal, secrets, socketserver, ssl, threading, time, urllib.parse, urllib.request, urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

class Capture:
    def __init__(self, work, owner):
        self.work, self.owner, self.lock = Path(work), owner, threading.Lock()
        self.path = self.work / 'captures.jsonl'
        self.signals = 0
        self.signal_id = secrets.token_hex(12)
        self.password = (self.work / 'smtp-password').read_text().strip()
        self.ownership_token = (self.work / 'fixture-owner-token').read_text().strip()
    def add(self, kind, payload):
        entry = {'kind':kind, 'at_ns':time.time_ns(), 'owner':self.owner, 'payload':payload}
        with self.lock:
            fd=os.open(self.path,os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
            with os.fdopen(fd,'a') as f: f.write(json.dumps(entry,ensure_ascii=False)+'\n')
        return entry
    def rows(self):
        with self.lock:
            return [json.loads(line) for line in self.path.read_text().splitlines()] if self.path.exists() else []

class SMTP(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(15)
        capture=self.server.capture
        def send(text): self.wfile.write((text+'\r\n').encode()); self.wfile.flush()
        send('220 localhost testlab capture ESMTP')
        authenticated=False; sender=''; recipients=[]
        while True:
            raw=self.rfile.readline(65537)
            if not raw or len(raw)>65536:return
            line=raw.decode('utf-8','replace').strip(); command=line.split(' ',1)[0].upper()
            if command in ('EHLO','HELO'):
                send('250-localhost'); send('250-AUTH PLAIN'); send('250 SIZE 1048576')
            elif command=='AUTH':
                parts=line.split(); value=parts[2] if len(parts)>2 else ''
                if not value:send('334 ');value=self.rfile.readline(4096).strip().decode()
                try:
                    auth=base64.b64decode(value,validate=True).split(b'\x00')
                    authenticated=len(auth)==3 and auth[1]==b'testlab' and auth[2].decode()==capture.password
                except Exception:authenticated=False
                send('235 2.7.0 authenticated' if authenticated else '535 5.7.8 denied')
            elif command=='MAIL':
                if not authenticated:send('530 authentication required');continue
                sender=line[5:];recipients=[];send('250 accepted')
            elif command=='RCPT':recipients.append(line[5:]);send('250 accepted')
            elif command=='DATA':
                if not authenticated or not sender or not recipients:send('503 bad sequence');continue
                send('354 finish with dot'); chunks=[]; size=0
                while True:
                    part=self.rfile.readline(65537)
                    if not part:return
                    if part==b'.\r\n':break
                    size+=len(part)
                    if size>1048576:return
                    chunks.append(part[1:] if part.startswith(b'..') else part)
                body=b''.join(chunks).decode('utf-8','replace')
                capture.add('smtp',{'sender':sender,'recipients':recipients,'body':body,'sha256':hashlib.sha256(body.encode()).hexdigest()})
                send('250 2.0.0 captured')
            elif command=='RSET':sender='';recipients=[];send('250 reset')
            elif command=='NOOP':send('250 ok')
            elif command=='QUIT':send('221 bye');return
            else:send('502 not implemented')

class TLSCaptureServer(socketserver.ThreadingTCPServer):
    allow_reuse_address=False; daemon_threads=True
    def get_request(self):
        conn,address=super().get_request()
        conn.settimeout(10)
        try:return self.tls_context.wrap_socket(conn,server_side=True),address
        except Exception:conn.close();raise

def handler(capture):
    class HTTP(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def reply(self,data,status=200,content='application/json'):
            payload=json.dumps(data,ensure_ascii=False).encode() if content=='application/json' else data.encode()
            self.send_response(status);self.send_header('Content-Type',content);self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
        def do_GET(self):
            path=self.path.split('?')[0]
            if path=='/health':
                challenge=urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query).get('challenge',[''])[0][:128]
                message=f'{challenge}|{os.getpid()}|{capture.owner}'.encode()
                proof=hmac.new(capture.ownership_token.encode(),message,hashlib.sha256).hexdigest()
                return self.reply({'owner':capture.owner,'mode':'owned_testlab','pid':os.getpid(),'proof':proof})
            if path=='/v1/models':return self.reply({'object':'list','data':[{'id':'testlab','object':'model','owned_by':'protocol_mock'},{'id':'testlab-embedding','object':'model','owned_by':'protocol_mock'}]})
            if path=='/captures':return self.reply({'items':capture.rows()})
            if path=='/metrics':
                with capture.lock: n=capture.signals; marker=capture.signal_id
                label='application="testlab",environment="test",service="testlab-api",signal_id="'+marker+'"'
                lines=['# TYPE testlab_requests_total counter',f'testlab_requests_total{{{label},status="200"}} {100+n}',f'testlab_requests_total{{{label},status="500"}} {n}', '# TYPE testlab_request_failures_total counter',f'testlab_request_failures_total{{{label}}} {n}', '# TYPE testlab_latency_seconds gauge',f'testlab_latency_seconds{{{label}}} 0.015']
                return self.reply('\n'.join(lines)+'\n',content='text/plain; version=0.0.4')
            if path=='/open-apis/contact/v3/users':return self.reply({'code':0,'data':{'items':[{'user_id':'fixture-user','open_id':'fixture-open','name':'Synthetic fixture','status':{'is_activated':True}}],'has_more':False}})
            if path.startswith('/wiki/api/v2/spaces/') and path.endswith('/pages'):return self.reply({'results':[{'id':'1','spaceId':'1','status':'current','title':'Protocol fixture','version':{'number':1},'body':{'storage':{'value':'<p>Synthetic test data</p>','representation':'storage'}}}],'_links':{}})
            if path.startswith('/v1/pages/'):
                if path.endswith('/markdown'):return self.reply({'object':'page_markdown','id':'00000000-0000-0000-0000-000000000001','markdown':'# Synthetic fixture\nExplicitly protocol_mock.','truncated':False})
                return self.reply({'object':'page','id':'00000000-0000-0000-0000-000000000001','archived':False,'in_trash':False,'last_edited_time':'2026-09-30T00:00:00Z','properties':{'title':{'type':'title','title':[{'plain_text':'Protocol fixture'}]}}})
            return self.reply({'error':'fixture route not implemented','evidence_mode':'protocol_mock'},404)
        def do_POST(self):
            size=int(self.headers.get('Content-Length','0'))
            if size<0 or size>1048576:return self.reply({'error':'too large'},413)
            raw=self.rfile.read(size)
            try:body=json.loads(raw) if raw else {}
            except ValueError:return self.reply({'error':'invalid JSON'},400)
            path=self.path.split('?')[0]
            if path=='/admin/shutdown':
                nonce=str(body.get('nonce',''));proof=str(body.get('proof',''))
                expected=hmac.new(capture.ownership_token.encode(),('shutdown|'+nonce+'|'+capture.owner).encode(),hashlib.sha256).hexdigest()
                if len(nonce)!=48 or not hmac.compare_digest(proof,expected):return self.reply({'error':'not owner'},403)
                self.reply({'stopping':True});threading.Timer(.2,lambda:os._exit(0)).start();return
            if path=='/signal':
                with capture.lock:capture.signals+=1;capture.signal_id=secrets.token_hex(12)
                return self.reply({'signals':capture.signals,'signal_id':capture.signal_id})
            if path.startswith('/capture/'):
                capture.add(path.removeprefix('/capture/'),body);return self.reply({'accepted':True})
            if path=='/open-apis/auth/v3/tenant_access_token/internal':return self.reply({'code':0,'tenant_access_token':'synthetic-fixture-token','expire':600})
            if path=='/open-apis/im/v1/messages':
                capture.add('feishu_protocol_mock',body);return self.reply({'code':0,'data':{'message_id':'synthetic-message'}})
            if path in ('/v1/embeddings','/embeddings'):
                inputs=body.get('input',[]);inputs=[inputs] if isinstance(inputs,str) else inputs
                if not isinstance(inputs,list) or len(inputs)>64 or any(not isinstance(x,str) for x in inputs):return self.reply({'error':'invalid bounded fixture input'},422)
                dim=body.get('dimensions',1024)
                if not isinstance(dim,int) or not 1<=dim<=4096:return self.reply({'error':'invalid fixture dimensions'},422)
                rows=[]
                for i,text in enumerate(inputs):
                    seed=hashlib.sha256(text.encode()).digest();vector=[(seed[j%32]-127.5)/127.5 for j in range(dim)];norm=math.sqrt(sum(x*x for x in vector));vector=[x/norm for x in vector]
                    rows.append({'object':'embedding','index':i,'embedding':vector})
                return self.reply({'object':'list','model':body.get('model','testlab-embedding'),'data':rows,'usage':{'prompt_tokens':len(inputs),'total_tokens':len(inputs)},'evidence_mode':'protocol_mock'})
            if path in ('/v1/chat/completions','/chat/completions'):
                result={'id':'protocol-mock-request','object':'chat.completion','model':body.get('model','protocol_mock'),'choices':[{'index':0,'message':{'role':'assistant','content':'Synthetic provider response. No real model was called.'},'finish_reason':'stop'}],'usage':{'prompt_tokens':10,'completion_tokens':12,'total_tokens':22}}
                if body.get('stream'):
                    if 'TESTLAB_FAULT_STREAM_HOLD' in json.dumps(body.get('messages',[])):
                        # Only a synthetic marker opts into a genuine long-lived provider socket.
                        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Cache-Control','no-cache');self.send_header('Connection','close');self.end_headers()
                        capture.add('model_fault_stream',{'evidence_mode':'protocol_mock','phase':'started','planned_seconds':35})
                        try:
                            for i in range(36):
                                chunk={'id':result['id'],'choices':[{'index':0,'delta':{'content':'Synthetic held stream '+str(i)+'\n'},'finish_reason':None}]}
                                self.wfile.write(('data: '+json.dumps(chunk)+'\n\n').encode());self.wfile.flush()
                                if i<35:time.sleep(1)
                            self.wfile.write(('data: '+json.dumps({'id':result['id'],'choices':[{'index':0,'delta':{},'finish_reason':'stop'}]})+'\n\ndata: [DONE]\n\n').encode());self.wfile.flush()
                            capture.add('model_fault_stream',{'evidence_mode':'protocol_mock','phase':'completed','planned_seconds':35})
                        except (BrokenPipeError,ConnectionResetError,OSError):
                            capture.add('model_fault_stream',{'evidence_mode':'protocol_mock','phase':'disconnected'})
                        self.close_connection=True;return
                    payload='data: '+json.dumps({'id':result['id'],'choices':[{'index':0,'delta':{'role':'assistant','content':result['choices'][0]['message']['content']},'finish_reason':None}]})+'\n\ndata: '+json.dumps({'id':result['id'],'choices':[{'index':0,'delta':{},'finish_reason':'stop'}],'usage':result['usage']})+'\n\ndata: [DONE]\n\n'
                    return self.reply(payload,content='text/event-stream')
                return self.reply(result)
            return self.reply({'error':'fixture route not implemented','evidence_mode':'protocol_mock'},404)
    return HTTP

def proxy_handler():
    class Proxy(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def relay(self):
            size=int(self.headers.get('Content-Length','0'))
            if size<0 or size>1048576:self.send_error(413);return
            raw=self.rfile.read(size) if size else None
            # Fixed upstream only, never a request-selected destination.
            path=urllib.parse.urlsplit(self.path)
            if path.scheme or path.netloc:self.send_error(400);return
            req=urllib.request.Request('http://127.0.0.1:18570'+self.path,raw,{'Content-Type':self.headers.get('Content-Type','application/json')},method=self.command)
            try:response=urllib.request.urlopen(req,timeout=10)
            except urllib.error.HTTPError as error:response=error
            except Exception:self.send_error(502);return
            with response:
                body=response.read(2*1024*1024)
                if len(body)>=2*1024*1024:self.send_error(502);return
                self.send_response(response.status);self.send_header('Content-Type',response.headers.get('Content-Type','application/json'));self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        do_GET=relay
        do_POST=relay
    return Proxy


def serve(work,owner,port,tls_port,smtp_port):
    capture=Capture(work,owner);context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(Path(work)/'tls-cert.pem',Path(work)/'tls-key.pem')
    http=ThreadingHTTPServer(('127.0.0.1',port),handler(capture))
    https=ThreadingHTTPServer(('127.0.0.1',tls_port),handler(capture));https.socket=context.wrap_socket(https.socket,server_side=True)
    prom=ThreadingHTTPServer(('127.0.0.1',18569),proxy_handler());prom.socket=context.wrap_socket(prom.socket,server_side=True)
    smtp=TLSCaptureServer(('127.0.0.1',smtp_port),SMTP);smtp.capture=capture;smtp.tls_context=context
    for server in (http,https,smtp,prom):threading.Thread(target=server.serve_forever,daemon=True).start()
    signal.pause()

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('command',choices=['serve']);p.add_argument('--work',required=True);p.add_argument('--owner',required=True);p.add_argument('--port',type=int,default=18578);p.add_argument('--tls-port',type=int,default=18579);p.add_argument('--smtp-port',type=int,default=18577)
    a=p.parse_args();serve(a.work,a.owner,a.port,a.tls_port,a.smtp_port)

"""TLS-only disposable SMTP sink; no relay, DNS delivery or outbound network."""
from __future__ import annotations
import base64,hmac,json,os,re,socketserver,ssl,threading,time
from pathlib import Path

MAX_MESSAGE=128*1024
MODES={'accept','reject_data','lose_data_response','lose_quit_response'}

def private_jsonl(path,record):
    fd=os.open(path,os.O_WRONLY|os.O_APPEND|os.O_CREAT|getattr(os,'O_NOFOLLOW',0),0o600)
    with os.fdopen(fd,'a') as stream:
        stream.write(json.dumps(record)+'\n');stream.flush();os.fsync(stream.fileno())

def local_address(value):
    match=re.fullmatch(r'<([A-Za-z0-9.!#$%&\x27*+/=?^_`{|}~-]+@testlab\.invalid)>',value,re.IGNORECASE)
    return match.group(1).lower() if match else None

class SMTPHandler(socketserver.StreamRequestHandler):
    def reply(self,line):
        self.wfile.write((line+'\r\n').encode());self.wfile.flush()

    def handle(self):
        self.connection.settimeout(15)
        authenticated=False;sender=None;recipient=None;accepted=False
        try:
            self.reply('220 localhost testlab SMTP sink')
            for _ in range(50):
                raw=self.rfile.readline(4097)
                if not raw or len(raw)>4096:return
                command=raw.decode('ascii',errors='strict').rstrip('\r\n')
                verb,_,argument=command.partition(' ');verb=verb.upper()
                if verb=='EHLO':
                    self.reply('250-localhost');self.reply('250-AUTH PLAIN');self.reply('250 SIZE 131072')
                elif verb=='AUTH':
                    method,_,encoded=argument.partition(' ')
                    if method.upper()!='PLAIN':self.reply('504 unsupported authentication');continue
                    if not encoded:
                        self.reply('334 ');encoded=self.rfile.readline(4097).decode('ascii').strip()
                    try:
                        authorization,username,password=base64.b64decode(encoded,validate=True).decode().split('\x00')
                        authenticated=(authorization in ('',self.server.config['username']) and
                            hmac.compare_digest(username,self.server.config['username']) and
                            hmac.compare_digest(password,self.server.config['password']))
                    except (ValueError,UnicodeError):authenticated=False
                    self.reply('235 authenticated' if authenticated else '535 invalid credentials')
                elif not authenticated:self.reply('530 authentication required')
                elif verb=='MAIL' and argument.upper().startswith('FROM:'):
                    envelope=re.fullmatch(r'(<[^>]+>)(?: SIZE=([0-9]+))?',argument[5:],re.IGNORECASE)
                    if envelope and envelope.group(2) and int(envelope.group(2))>MAX_MESSAGE:
                        sender=recipient=None;self.reply('552 message exceeds local bound');continue
                    sender=local_address(envelope.group(1)) if envelope else None;recipient=None;accepted=False
                    self.reply('250 local sender accepted' if sender else '550 local testlab domain required')
                elif verb=='RCPT' and argument.upper().startswith('TO:'):
                    recipient=local_address(argument[3:]) if sender else None
                    self.reply('250 local recipient accepted' if recipient else '550 local testlab domain required')
                elif verb=='DATA' and sender and recipient:
                    self.reply('354 end with dot')
                    chunks=[];size=0
                    while True:
                        line=self.rfile.readline(4097)
                        if not line or len(line)>4096:return
                        if line==b'.\r\n':break
                        if line.startswith(b'..'):line=line[1:]
                        size+=len(line)
                        if size>MAX_MESSAGE:self.reply('552 message exceeds local bound');return
                        chunks.append(line)
                    mode=json.loads((self.server.work/'mode.json').read_text())['mode']
                    if mode not in MODES:self.reply('451 invalid local mode');return
                    outcome={'accept':'accepted','reject_data':'rejected_data','lose_data_response':'accepted_lost_response',
                             'lose_quit_response':'accepted_quit_lost'}[mode]
                    record={'at':time.time(),'sender':sender,'recipient':recipient,'outcome':outcome,
                            'message':b''.join(chunks).decode('utf-8'),'size':size}
                    with self.server.capture_lock:private_jsonl(self.server.work/'captures.jsonl',record)
                    if mode=='reject_data':self.reply('550 synthetic DATA refusal')
                    elif mode=='lose_data_response':return
                    else:self.reply('250 local sink accepted');accepted=True
                elif verb=='QUIT':
                    mode=json.loads((self.server.work/'mode.json').read_text())['mode']
                    if accepted and mode=='lose_quit_response':return
                    self.reply('221 bye');return
                elif verb=='RSET':sender=recipient=None;accepted=False;self.reply('250 reset')
                elif verb=='NOOP':self.reply('250 ok')
                else:self.reply('503 invalid SMTP state')
        except (OSError,UnicodeError,ValueError,KeyError):
            # Credentials and invitation bodies are never written to stdout.
            return

class SMTPServer(socketserver.ThreadingTCPServer):
    allow_reuse_address=True
    daemon_threads=True
    def __init__(self,work,port=465,host='0.0.0.0'):
        self.work=Path(work);self.config=json.loads((self.work/'smtp-config.json').read_text())
        self.capture_lock=threading.Lock();self.slots=threading.BoundedSemaphore(16)
        self.tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);self.tls.minimum_version=ssl.TLSVersion.TLSv1_2
        self.tls.load_cert_chain(self.work/'smtp.crt',self.work/'smtp.key')
        super().__init__((host,port),SMTPHandler)
    def get_request(self):
        conn,address=super().get_request();conn.settimeout(10)
        try:return self.tls.wrap_socket(conn,server_side=True),address
        except OSError:conn.close();raise
    def process_request(self,request,address):
        if not self.slots.acquire(blocking=False):request.close();return
        try:super().process_request(request,address)
        except BaseException:self.slots.release();raise
    def process_request_thread(self,request,address):
        try:super().process_request_thread(request,address)
        finally:self.slots.release()
    def handle_error(self,*args):pass

if __name__=='__main__':
    import sys
    with SMTPServer(Path(sys.argv[1])) as server:server.serve_forever()

"""Software ES256 authenticator. Real WebAuthn bytes/signatures, no hardware claim."""
import base64,hashlib,json,os,secrets,struct,subprocess
from pathlib import Path

def b64(raw):return base64.urlsafe_b64encode(raw).decode().rstrip('=')
def cbor(v):
    def head(major,n):
        if n<24:return bytes([major<<5|n])
        if n<256:return bytes([major<<5|24,n])
        if n<65536:return bytes([major<<5|25])+struct.pack('>H',n)
        return bytes([major<<5|26])+struct.pack('>I',n)
    if isinstance(v,int):return head(0,v) if v>=0 else head(1,-1-v)
    if isinstance(v,bytes):return head(2,len(v))+v
    if isinstance(v,str):r=v.encode();return head(3,len(r))+r
    if isinstance(v,dict):return head(5,len(v))+b''.join(cbor(k)+cbor(x) for k,x in v.items())
    raise ValueError('bounded authenticator CBOR type unsupported')

class Authenticator:
    def __init__(self,work,name,rp='localhost',origin='https://localhost:18583'):
        self.rp,self.origin,self.counter=rp,origin,0;self.id=secrets.token_bytes(32);self.key=Path(work)/(name+'-software-passkey.pem')
        if self.key.exists():raise RuntimeError('fresh software authenticator file required')
        p=subprocess.run(['openssl','genpkey','-algorithm','EC','-pkeyopt','ec_paramgen_curve:P-256','-out',str(self.key)],capture_output=True)
        if p.returncode:raise RuntimeError('software ES256 key generation failed')
        os.chmod(self.key,0o600)
        raw=subprocess.check_output(['openssl','pkey','-in',str(self.key),'-pubout','-outform','DER']);self.public=raw[-65:]
        if self.public[0]!=4:raise RuntimeError('software ES256 public encoding invalid')
    def save(self,path):
        state={'rp':self.rp,'origin':self.origin,'counter':self.counter,'credential_id':b64(self.id),'key':str(self.key)}
        Path(path).write_text(json.dumps(state));os.chmod(path,0o600)
    @classmethod
    def restore(cls,path,work,rp,origin):
        path=Path(path)
        if path.is_symlink() or path.stat().st_mode&0o777!=0o600:raise RuntimeError('private software authenticator state required')
        state=json.loads(path.read_text());key=Path(state['key'])
        if state['rp']!=rp or state['origin']!=origin or key.is_symlink() or key.parent.resolve()!=Path(work).resolve() or key.stat().st_mode&0o777!=0o600:raise RuntimeError('software authenticator ownership mismatch')
        a=cls.__new__(cls);a.rp=rp;a.origin=origin;a.counter=state['counter'];a.key=key;a.id=base64.urlsafe_b64decode(state['credential_id']+'='*((4-len(state['credential_id'])%4)%4))
        if len(a.id)!=32 or not isinstance(a.counter,int) or a.counter<0:raise RuntimeError('software credential state invalid')
        return a
    def client(self,kind,challenge):return json.dumps({'type':kind,'challenge':challenge,'origin':self.origin,'crossOrigin':False},separators=(',',':')).encode()
    def auth(self,flags,counter,tail=b''):return hashlib.sha256(self.rp.encode()).digest()+bytes([flags])+struct.pack('>I',counter)+tail
    def credential(self,response):return {'id':b64(self.id),'rawId':b64(self.id),'type':'public-key','response':response}
    def register(self,challenge):
        key=cbor({1:2,3:-7,-1:1,-2:self.public[1:33],-3:self.public[33:]});tail=b'\0'*16+struct.pack('>H',len(self.id))+self.id+key
        att=cbor({'fmt':'none','attStmt':{},'authData':self.auth(0x45,0,tail)})
        return self.credential({'attestationObject':b64(att),'clientDataJSON':b64(self.client('webauthn.create',challenge))})
    def assertion(self,challenge):
        self.counter+=1;auth=self.auth(0x05,self.counter);client=self.client('webauthn.get',challenge)
        p=subprocess.run(['openssl','dgst','-sha256','-sign',str(self.key)],input=auth+hashlib.sha256(client).digest(),capture_output=True)
        if p.returncode:raise RuntimeError('software ES256 signing failed')
        return self.credential({'authenticatorData':b64(auth),'clientDataJSON':b64(client),'signature':b64(p.stdout)})

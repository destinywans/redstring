"""Six HTTP: native connector create, duplicate tools PATCH and audit rollback.
No SQL business seed, Prober, provider HTTP, executor or complete chain.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-conversation-reads-storage-failure';PORTS=(18956,18957)
def load(name,path):
 spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
h=load('query_boundary_private_upload',ROOT/'tools/testlab/api_upload_audit_rollback_coverage.py');gate=load('query_boundary_private_gate',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
ro=h.ro;check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=h.SOURCE_SHA,h.BINARY_SHA,h.RUNTIME_SHA,h.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/conversations'),('GET','/conversations/{id}'),('GET','/mentions/{kind}/{id}/preview'));SUPPORT=(('POST','/auth/login'),('POST','/conversations'))
CASES=('list','detail','preview')
REQUIRED_CASES=tuple('conversation_reads.'+n for n in ('identity','created',*(kind+'.'+phase for kind in CASES for phase in ('baseline','repeat','fault','restore','recovery')),'ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_conversation_reads_storage_failure.py','tests/test_api_conversation_reads_storage_failure.py','api_upload_audit_rollback_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalConversationReadsStorageFailure',proof_scope='ordinary empty own conversation list/detail/mention reads and precise SELECT ACL failures; no turn/model/SSE/whole acceptance')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(ro.Contract):
 def canonical(self,method,path):
  parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.fragment)),'relative exact boundary path required')
  check(not parsed.query,'exact connector tools path only')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside connector tools/create/login');return canonical,kind
ro.Contract=Contract
_src=inspect.getsource(gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-conversation-reads-storage-failure'),('conversation_boundaries_authorized','conversation_reads_storage_failure_authorized')):
 check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
def doctor(root):
 data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift')
 info=ro.inspect_pg(data['postgres_container']);check(info['Id']==PG_CID and info['HostConfig'].get('NanoCpus')==4000000000 and info['HostConfig'].get('Memory')==2147483648,'exact PG4CPU2GiB required')
 data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());h.frozen_identity(data,info['Id']);return data

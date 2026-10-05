"""Source-declared API inventory, kept separate from actual HTTP evidence."""
from __future__ import annotations
import json
from pathlib import Path

SCENARIOS = ('normal','authorization','boundary','idempotency','failure')

def interfaces(root):
    contract=json.loads((Path(root)/'internal/apicontract/openapi.json').read_text())
    base=contract['servers'][0]['url'].rstrip('/')
    if not base.startswith('/') or '://' in base:raise ValueError('local relative API contract base required')
    for path,methods in sorted(contract['paths'].items()):
        for method,operation in sorted(methods.items()):
            if method.upper() not in ('GET','POST','PUT','PATCH','DELETE'):continue
            yield {'method':method.upper(),'path':base+path,'module':operation.get('tags',['未分类'])[0],
                   'operation_id':operation['operationId'],'evidence_mode':'source',
                   'registration_evidence':False,'scenarios':{name:{'status':'queued'} for name in SCENARIOS},
                   'note':'源码契约声明；包含历史和条件式路由，正式V3注册及五类场景须分别核验'}

def actual_interface(row):
    mode=row.get('evidence_mode','real_local')
    if mode not in ('real_local','protocol_mock') or (row.get('upstream_mode')=='protocol_mock' and mode!='protocol_mock'):
        raise ValueError('explicit HTTP evidence mode must preserve protocol Mock scope')
    scenario=row['scenario']
    if scenario not in SCENARIOS:raise ValueError('unknown interface scenario')
    actual=row['actual'];expected=row['expected']
    actual_status=actual.get('status') if isinstance(actual,dict) else actual
    expected_status=expected.get('status') if isinstance(expected,dict) else expected
    expected_codes=expected_status if isinstance(expected_status,(list,tuple)) else [expected_status]
    if type(actual_status) is not int or not 100<=actual_status<=599 or not expected_codes or any(type(code) is not int or not 100<=code<=599 for code in expected_codes):
        raise ValueError('actual HTTP status and expected status codes required')
    if row['status'] not in ('passed','failed') or (row['status']=='passed' and actual_status not in expected_codes):
        raise ValueError('interface outcome contradicts actual HTTP status')
    method=row['method'];path=row['path']
    if method not in ('GET','POST','PUT','PATCH','DELETE') or not isinstance(path,str) or not path.startswith('/api/') or any(marker in path for marker in ('://','?','#')):
        raise ValueError('canonical local interface method/path required')
    # 401/403 can originate in middleware before a handler is selected.
    # An explicit refusal stays false; only legacy rows may infer it from 2xx.
    registered=row.get('registration_evidence') is not False and (bool(row.get('registration_evidence')) or (row['status']=='passed' and 200<=actual_status<300)) and actual_status not in (401,403,404)
    return {'method':method,'path':path,'evidence_mode':mode,
            'scenarios':{scenario:{'status':row['status'],'expected_status':expected_codes[0] if len(expected_codes)==1 else list(expected_codes),'actual_status':actual_status,'expected_detail':expected,'actual_detail':actual}},
            'registration_evidence':registered,'test_name':row.get('test_name'),
            'upstream_mode':row.get('upstream_mode'), 'proof_scope':row.get('proof_scope',''),
            'note':'正式双V3实例真实HTTP断言；'+('响应支持已注册路径' if registered else '本次响应不单独证明路由注册')}

def attach_defects(store,run_id):
    path=store.root/'tools/testlab/defects.json'
    if not path.exists():return
    for row in json.loads(path.read_text()):
        row=dict(row);row['evidence_ids']=[]
        for relative in row.pop('local_evidence',[]):
            file=(store.root/relative).resolve()
            if file.is_relative_to(store.data) and file.is_file() and not file.is_symlink():
                row['evidence_ids'].append(store.write_evidence(run_id,file.read_text(errors='replace')))
        store.add_record(run_id,'defects',row)

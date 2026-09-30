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
    scenario=row['scenario']
    if scenario not in SCENARIOS:raise ValueError('unknown interface scenario')
    # 401/403 can originate in middleware before a handler is selected.
    registered=bool(row.get('registration_evidence')) and row.get('actual') not in (401,403,404)
    return {'method':row['method'],'path':row['path'],'evidence_mode':'real_local',
            'scenarios':{scenario:{'status':row['status'],'expected_status':row['expected'],'actual_status':row['actual']}},
            'registration_evidence':registered,'test_name':row.get('test_name'),
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

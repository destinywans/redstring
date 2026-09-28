#!/usr/bin/env python3
"""Check references and registered routes without installing a frontend toolchain."""
import json, re
from pathlib import Path
root=Path(__file__).resolve().parent.parent
spec=json.loads((root/'internal/apicontract/openapi.json').read_text())
assert spec['openapi']=='3.1.0'
ids=set()
def walk(v):
    if isinstance(v,dict):
        if '$ref' in v:
            ref=v['$ref'];assert ref.startswith('#/'),ref
            target=spec
            for part in ref[2:].split('/'):target=target[part.replace('~1','/').replace('~0','~')]
        for x in v.values():walk(x)
    elif isinstance(v,list):
        for x in v:walk(x)
walk(spec)
declared=set()
for path,methods in spec['paths'].items():
    for method,op in methods.items():
        declared.add((method.upper(),'/api/v1'+path))
        assert op['operationId'] not in ids,op['operationId']
        ids.add(op['operationId'])
        assert 'responses' in op and 'description' in op
actual=set()
for directory in ('httpapi','actions','knowledge','operations','delivery','deployment','memoryview','aiextensions','resources','settings','profile','rbac',
                  'v3admin','v3alertmanager','v3alertrules','v3audit','v3canvas','v3catalog','v3conversation','v3discovery','v3events','v3integrations','v3knowledge','v3knowledgesources','v3sourceregistry','v3mcp','v3observe','v3postmortems','v3proposals','v3settings','v3topology','v3uploads','v3webhook'):
    for f in (root/'internal'/directory).glob('*.go'):
        if f.name.endswith('_test.go'):continue
        actual.update(re.findall(r'"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^\"]+)"',f.read_text()))
for f in (root/'cmd/aiops').glob('*.go'):
    actual.update(re.findall(r'"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^\"]+)"',f.read_text()))
assert actual==declared,{'undocumented':sorted(actual-declared),'unimplemented':sorted(declared-actual)}
print(f'Contract references and {len(declared)} registered operations match.')

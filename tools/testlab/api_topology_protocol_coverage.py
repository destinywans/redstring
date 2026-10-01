"""Owned native inventory ingestion and full topology HTTP protocol acceptance.

All topology sources are explicitly synthetic protocol Mock. No real call graph,
traffic, authority exporter, historical retention or 24h result is asserted.
Only public HTTP creates services/connectors/targets; native importer writes graph.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import copy
import datetime as dt
import fcntl
import functools
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import sqlite3
import stat
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlencode, urlsplit

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
SOURCE_SHA = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
GITLAB_CID = 'e348f0a8682bd85219d878864569f54111a1d3195e24bb3e8b143b407b2356a6'
GITLAB_IMAGE = 'sha256:17e6f56bb2a7307ad60073e2a26e82734ff80d729370e67d68067db298365b39'
PORTS = (18698, 18699)
RESERVED_PORTS = tuple(range(18698, 18705))
METRICS_PORT = 18700
BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)
MODULE = 'formal-api-topology-protocol'
TARGETS = (('GET', '/topology'),)
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/connectors'),
           ('POST', '/connectors/{id}/test'), ('POST', '/services/{id}/external-targets'))
REQUIRED = ('topology.native_import', 'topology.normal', 'topology.authorization',
            'topology.boundary', 'topology.source_faults', 'topology.graph_revision',
            'topology.grant_revocation', 'topology.session_revocation', 'topology.audit')
_spec = importlib.util.spec_from_file_location('topology_private_readonly', ROOT / 'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, BASES, ()
Blocked, check = ro.Blocked, ro.check
ACTIVE = None
_thread = threading.local()
_append = ro.append_json
_transport = ro.transport


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def hashes():
    names = ('api_topology_protocol_coverage.py', 'api_readonly_chain.py', 'runner.py', 'backend.py', 'env.py', 'faults.py')
    return {'tools/testlab/' + n: hashlib.sha256((ROOT / 'tools/testlab' / n).read_bytes()).hexdigest() for n in names}


def stamp(row):
    mode = getattr(_thread, 'mode', 'real_local')
    return dict(row, evidence_mode=mode, upstream_mode=mode,
                proof_scope='Synthetic source graph and metrics protocol Mock; no real call graph or traffic' if mode == 'protocol_mock' else 'Owned native local identity/configuration support')


def emit(name, status, actual, **fields):
    if ACTIVE is not None and name.startswith('readonly.api_ready.'):
        name = name.replace('readonly.api_ready.', 'topology.api_ready.' + ACTIVE.stage + '.', 1)
    mode = fields.pop('evidence_mode', getattr(_thread, 'mode', 'real_local'))
    if isinstance(fields.get('interface'), dict):
        fields['interface'].update(stamp(fields['interface']))
    row = dict(name=name, status=status, actual=actual, module=MODULE,
               evidence_mode=mode, upstream_mode=mode, **fields)
    if ACTIVE is not None:
        _append(ACTIVE.work / 'cases.jsonl', row)
    print(json.dumps(row, ensure_ascii=False), flush=True)


def append(path, row):
    _append(path, stamp(row) if Path(path).name == 'interfaces.jsonl' else row)


def transport(*args, **kwargs):
    status, body, headers = _transport(*args, **kwargs)
    if ACTIVE is not None and hasattr(_thread, 'call_id'):
        ro._private(ACTIVE.work / ('http-response-' + str(_thread.call_id) + '.json'),
                    stamp(dict(method=args[1], path=args[2], status=status, response=json.loads(body) if body else None,
                               source_tree_sha=SOURCE_SHA, headers={k: v for k, v in headers.items() if k.lower() in ('content-type', 'cache-control')})))
    return status, body, headers


ro.emit, ro.append_json, ro.transport = emit, append, transport


def resource_id(kind, source, uid):
    return 'inventory-' + hashlib.sha256((kind + '\0' + source + '\0' + uid).encode()).hexdigest()[:40]


def snapshot(nonce, app, env_id, observed_at):
    sid = 'topology-' + nonce
    return {'sources': [dict(source_id=sid, source_kind='docker', source_name='Owned protocol graph ' + nonce,
        environment_key='test', observed_at=observed_at,
        resources=[dict(external_id=nonce + '-' + key, kind='host', name='Protocol ' + key,
            observed_at=observed_at, attributes=dict(cluster='owned-' + nonce, namespace='owned', zone='protocol-zone'),
            applications=[dict(application_id=app, environment_id=env_id)]) for key in ('A', 'B')],
        relations=[dict(source_external_id=nonce + '-A', target_external_id=nonce + '-B', kind='calls')])]}


def matrix(graph, app, start, end, step, mode='normal'):
    check(step == 60 and end - start == 300, 'bounded six-point protocol window required')
    common = dict(schema='topology_v1', application_id=app, environment_id='test')
    rows = []
    def add(field, unit, labels, value):
        rows.append(dict(metric={**common, 'field': field, 'unit': unit, **labels},
                         values=[[start + i * step, str(value if not isinstance(value, list) else value[i])] for i in range(6)]))
    for key in ('A', 'B'):
        n = graph[key]
        labels = dict(resource_id=n['id'], external_uid=n['external_id'], source_kind=n['source_kind'], source_id=n['source_id'])
        add('node_health', 'state', labels, 1 if key == 'B' else 0)
        add('node_impacted', 'bool', labels, 1 if key == 'B' else 0)
    edge = graph['edge']
    add('edge_rps', 'requests_per_second', dict(relation_id=edge['id'], from_resource_id=graph['A']['id'],
        to_resource_id=graph['B']['id'], relation_revision=edge['revision']), 25)
    add('edge_error_pct', 'percent', dict(relation_id=edge['id'], from_resource_id=graph['A']['id'],
        to_resource_id=graph['B']['id'], relation_revision=edge['revision']), 2)
    n = graph['B']
    labels = dict(resource_id=n['id'], external_uid=n['external_id'], source_kind=n['source_kind'], source_id=n['source_id'])
    for field, unit, value in [('center_connections_used', 'count', 3), ('center_connections_max', 'count', 12),
                             ('center_wait_p95_ms', 'milliseconds', [1, 2, 3, 4, 5, 6])]:
        add(field, unit, labels, value)
    add('center_from_used', 'count', {**labels, 'caller_id': graph['A']['id']}, 2)
    add('center_from_max', 'count', {**labels, 'caller_id': graph['A']['id']}, 8)
    if mode == 'identity':
        rows[0]['metric']['external_uid'] = 'foreign'
    elif mode == 'cross_scope':
        rows[0]['metric']['application_id'] = 'foreign'
    elif mode == 'missing':
        rows.pop()
    elif mode == 'grid':
        rows[0]['values'][0][0] += 2
    elif mode == 'revision':
        rows[4]['metric']['relation_revision'] = '2000-01-01T00:00:00Z'
    elif mode == 'zero_rps':
        rows[4]['values'] = [[t, '0'] for t, _ in rows[4]['values']]
    elif mode == 'labels':
        rows[0]['metric']['unexpected'] = 'extra'
    elif mode == 'duplicate':
        rows.append(copy.deepcopy(rows[0]))
    elif mode == 'bounds':
        rows[6]['values'] = [[t, '999'] for t, _ in rows[6]['values']]
    elif mode not in ('normal', 'graph_change', 'revoke_grant', 'revoke_session', '503', 'warning'):
        raise Blocked('unknown topology fixture fault')
    result = dict(status='success', data=dict(resultType='matrix', result=rows))
    if mode == 'warning':
        result['warnings'] = ['explicit incomplete protocol fixture']
    return result


@functools.lru_cache(maxsize=1)
def topology_schemas():
    return json.loads((ROOT/'internal/apicontract/openapi.json').read_text())['components']['schemas']


def validate_schema(value, schema, components):
    """Validate the frozen topology schema's recursive required/type/bounds subset."""
    if '$ref' in schema:
        ref=schema['$ref']
        check(ref.startswith('#/components/schemas/V3Topology'), 'only frozen local topology schema refs allowed')
        return validate_schema(value,components[ref.rsplit('/',1)[-1]],components)
    kind=schema.get('type')
    valid={'object':isinstance(value,dict),'array':isinstance(value,list),'string':isinstance(value,str),
           'integer':type(value) is int,'number':type(value) in (int,float) and math.isfinite(value),
           'boolean':type(value) is bool}
    check(valid.get(kind,False),'nested frozen topology value type differs')
    if kind=='object':
        check(set(schema.get('required',[])).issubset(value),'nested frozen topology required fields missing')
        properties=schema.get('properties',{})
        check(schema.get('additionalProperties') is not False or set(value).issubset(properties),'unexpected nested topology fields')
        for name,child in value.items():
            if name in properties:
                validate_schema(child,properties[name],components)
    elif kind=='array':
        for child in value:
            validate_schema(child,schema['items'],components)
    if 'enum' in schema:
        check(value in schema['enum'],'nested topology enum differs')
    if kind in ('integer','number'):
        check('minimum' not in schema or value>=schema['minimum'],'nested topology minimum violated')
        check('maximum' not in schema or value<=schema['maximum'],'nested topology maximum violated')


def validate_view(value, graph):
    check(isinstance(value, dict) and set(value) == {'groups', 'nodes', 'edges', 'blast', 'tree', 'sankey', 'detail'}, 'complete frozen topology field set required')
    schemas=topology_schemas()
    validate_schema(value,schemas['V3Topology'],schemas)
    nodes = {n['id']: n for n in value['nodes']}
    check(set(nodes) == {graph['A']['id'], graph['B']['id']} and len(value['nodes']) == 2, 'exact nonempty owned node set required')
    for key, status, impacted in [('A', 'ok', False), ('B', 'warn', True)]:
        n = nodes[graph[key]['id']]
        check(n['name'] == graph[key]['name'] and n['brand'] == 'docker' and n['status'] == status and n['impacted'] is impacted,
              'node source/name/health/impact differs')
        check(type(n['x']) is int and type(n['y']) is int and 0 < n['x'] < 880 and 0 < n['y'] < 760, 'node geometry outside frozen viewport')
    check(len(value['groups']) == 1 and {n['group'] for n in nodes.values()} == {value['groups'][0]['id']}, 'group references mismatch')
    check(len(value['edges']) == 1, 'exact positive-flow relation required')
    edge = value['edges'][0]
    check(edge['id'] == graph['edge']['id'] and edge['from'] == graph['A']['id'] and edge['to'] == graph['B']['id'] and
          edge['label'] == '25/s · 2.0%' and edge['tone'] == 'warn' and edge['fast'] is False and edge['width'] > 0 and bool(edge['path']), 'relation identity/flow projection mismatch')
    check(value['blast'] == '1 observed impacted resources / 2 visible resources', 'observed impact count differs')
    check(len(value['tree']) >= 2 and {graph['A']['name'], graph['B']['name']}.issubset({r['name'] for r in value['tree']}), 'complete tree records absent')
    sankey = value['sankey']
    check(len(sankey['cols']) == 2 and all(len(c) == 1 for c in sankey['cols']) and len(sankey['links']) == 1, 'complete two-column Sankey required')
    check(sankey['links'][0]['from'] == 'src:' + graph['A']['id'] and sankey['links'][0]['to'] == 'dst:' + graph['B']['id'] and
          sankey['links'][0]['value'] == 25 and all(c[0]['value'] == 25 for c in sankey['cols']), 'Sankey flow identity/value differs')
    check(sankey['cols'][0][0]['id']=='src:'+graph['A']['id'] and sankey['cols'][1][0]['id']=='dst:'+graph['B']['id'] and
          sankey['cols'][0][0]['label']==graph['A']['name'] and sankey['cols'][1][0]['label']==graph['B']['name'],'Sankey node identity/name differs')
    detail = value['detail']
    check(detail['id'] == graph['B']['id'] and detail['name'] == graph['B']['name'] and detail['brand'] == 'docker' and
          detail['conns'] == {'used': 3, 'max': 12} and detail['from_service'] == {'used': 2, 'max': 8, 'service': graph['A']['name']} and
          detail['wait'] == [1, 2, 3, 4, 5, 6] and detail['health'] == [{'k': 'Status', 'v': 'warn'}] and
          detail['source'] == 'docker · ' + graph['B']['source_id'] and detail['dependents'] == str(int(graph['edge']['kind'] == 'depends_on')) + ' visible explicit dependents', 'center/caller/source/health fields differ')


class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment or not parsed.path.startswith('/api/v1/'):
            raise Blocked('formal local topology namespace required')
        short = parsed.path[len('/api/v1'):]
        candidates = [route for verb, route in (*TARGETS, *SUPPORT) if method == verb and
                      re.fullmatch(re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(route)), short)]
        route = short if short in candidates else candidates[0] if len(candidates) == 1 else None
        if route is None or method.lower() not in self.api['paths'].get(route, {}):
            raise Blocked('outside bounded topology support contract')
        return '/api/v1' + route, 'main' if (method, route) in self.main else 'auxiliary_or_historical'


class Protocol:
    def __init__(self, chain):
        self.chain, self.mode = chain, 'normal'
        self.server = self.thread = None
        self.release = threading.Event()
        self.records = []

    def respond(self, path):
        parsed = urlsplit(path)
        check(not parsed.scheme and not parsed.netloc and not parsed.fragment, 'only local relative protocol path allowed')
        if path == '/-/ready':
            return 200, {'ready': True}
        q = parse_qs(parsed.query, keep_blank_values=True)
        check(parsed.path == '/api/v1/query_range' and set(q) == {'query', 'start', 'end', 'step', 'timeout', 'limit'} and
              all(len(v) == 1 for v in q.values()) and q['query'] == [self.chain.expression] and q['step'] == ['60'] and
              q['timeout'] == ['5s'] and q['limit'] == ['100'], 'exact reviewed scoped metric query required')
        start, end = [dt.datetime.fromisoformat(q[k][0].replace('Z', '+00:00')).timestamp() for k in ('start', 'end')]
        check(abs(end - time.time()) < 120 and end % 60 == 0, 'current minute-aligned metric window required')
        if self.mode == '503':
            return 503, {'error': 'explicit protocol fault'}
        body = matrix(self.chain.graph, self.chain.app, start, end, 60, self.mode)
        if self.mode in ('graph_change', 'revoke_grant', 'revoke_session'):
            self.chain.inflight_fault(self.mode)
        return 200, body

    def start(self):
        owner = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_GET(self):
                try:
                    status, body = owner.respond(self.path)
                except BaseException as error:
                    status, body = 500, {'error': type(error).__name__}
                row = dict(path=self.path, status=status, mode=owner.mode, evidence_mode='protocol_mock', upstream_mode='protocol_mock')
                owner.records.append(row)
                _append(owner.chain.work / 'source-reads.jsonl', row)
                raw = json.dumps(body).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                try:
                    self.wfile.write(raw)
                except (BrokenPipeError, ConnectionResetError):
                    pass
            def do_POST(self):
                self.send_error(405)
            do_PUT = do_PATCH = do_DELETE = do_POST
        self.server = ThreadingHTTPServer(('127.0.0.1', METRICS_PORT), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': .05}, daemon=True)
        self.thread.start()

    def close(self, deadline):
        self.release.set()
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
        if self.thread is not None:
            self.thread.join(timeout=max(0, deadline - time.monotonic()))
            check(not self.thread.is_alive(), 'held protocol server cleanup incomplete')


def free_ports():
    for port in RESERVED_PORTS:
        with socket.socket() as s:
            s.settimeout(.2)
            if s.connect_ex(('127.0.0.1', port)) == 0:
                raise Blocked('exclusive topology port occupied: ' + str(port))


@contextlib.contextmanager
def exclusive(root):
    folder = Path(root).resolve() / '.local/testlab/topology-protocol-parallel'
    if folder.resolve() != folder:
        raise Blocked('topology lease path traverses symlink')
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(folder / 'lease', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular lease required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('topology protocol package already active') from None
        yield
    finally:
        os.close(fd)


def active_runs(root, allow_current_uuid=None):
    allowed_scripts = {'tools/testlab/api_rule_assistant_container_coverage.py', 'tools/testlab/promotion_chain.py'}
    db = Path(root) / '.local/testlab/state.sqlite3'
    check(db.is_file() and not db.is_symlink(), 'existing regular run ledger required')
    connection = sqlite3.connect(db.resolve().as_uri() + '?mode=ro', uri=True)
    found = []
    try:
        for rid, raw in connection.execute('SELECT id,data FROM runs'):
            run = json.loads(raw)
            if run.get('status') not in ('queued', 'running'):
                continue
            records = [json.loads(row[0]) for row in connection.execute("SELECT data FROM records WHERE run_id=? AND kind='environment'", (rid,))]
            paths = [Path(root) / r['manifest'] for r in records if r.get('kind') == 'source' and r.get('manifest')]
            check(len(paths) == 1, 'active run needs one exact manifest source')
            path = paths[0]
            check(path.resolve().is_relative_to(Path(root).resolve() / '.local/testlab') and not path.is_symlink() and
                  stat.S_IMODE(path.stat().st_mode) == 0o600 and hashlib.sha256(path.read_bytes()).hexdigest() == run.get('manifest_sha'), 'active manifest identity/SHA mismatch')
            manifest = json.loads(path.read_text())
            selected = run.get('selected_suite', '')
            suites = manifest.get('suites', [])
            suites = suites if selected == 'full' else [s for s in suites if s['id'] in selected.split(',')]
            check(suites and (selected == 'full' or {s['id'] for s in suites} == set(selected.split(','))) and
                  run.get('source_tree_sha') == SOURCE_SHA, 'active selected suites/source identity mismatch')
            commands = [c.get('argv', []) for s in suites for c in s.get('commands', [])]
            check(commands and all(len(c) > 1 and c[0] in ('python3', 'python') for c in commands), 'active commands need exact authorized Python script')
            scripts = {c[1] for c in commands}
            current = bool(allow_current_uuid and scripts == {'tools/testlab/api_topology_protocol_coverage.py'} and
                           len(commands) == 1 and '--run-id' in commands[0] and
                           commands[0][commands[0].index('--run-id') + 1] == allow_current_uuid and run.get('pid') == os.getppid())
            check(current or scripts and scripts.issubset(allowed_scripts), 'unknown or duplicate active run; refusing new launch')
            found.append(dict(run_id=rid, manifest=str(path), manifest_sha=run['manifest_sha'], scripts=sorted(scripts), current=current))
    finally:
        connection.close()
    return found


def gitlab_remaining_reserve(info, cgroup):
    limit = 4 * 1024**3
    if (info.get('Id') != GITLAB_CID or info.get('Image') != GITLAB_IMAGE or
        info.get('Name') != '/aiops-' + OWNER + '-gitlab' or info.get('Config',{}).get('Labels',{}).get('aiops.testlab.owner') != OWNER or
        info.get('State',{}).get('Running') is not True or info.get('HostConfig',{}).get('Memory') != limit or
        info.get('HostConfig',{}).get('NanoCpus') != 2_000_000_000):
        return limit, {'unattested_or_stopped': True}
    anon = cgroup.get('anon')
    if type(anon) is not int or not 0 <= anon <= limit or cgroup.get('memory_max') != limit or cgroup.get('cpu_quota') != 2:
        return limit, {'anonymous_or_limits_unattested': True}
    return limit - anon, dict(cid=GITLAB_CID,image=GITLAB_IMAGE,fixed_limit_bytes=limit,
                             anonymous_resident_bytes=anon,remaining_limit_reserved_bytes=limit-anon,
                             operation='read-only accounting; no adoption or mutation; file cache not deducted')


def resource_admission(vm_available, host_available, host_pressure, pending_gitlab_bytes=4*1024**3):
    if type(pending_gitlab_bytes) is not int or not 0 <= pending_gitlab_bytes <= 4 * 1024**3:
        raise Blocked('remaining GitLab reservation is invalid')
    if vm_available < pending_gitlab_bytes + int(1.125 * 1024**3) + 1024**3:
        raise Blocked('Docker VM available below GitLab4GiB+rule1.125GiB+reserve1GiB')
    if host_available < 2 * 1024**3 or host_pressure != 1:
        raise Blocked('host below two-API reserve or memory pressure not normal')
    return dict(vm_available_bytes=vm_available, host_available_bytes=host_available, host_pressure=host_pressure,
                gitlab_reserve_bytes=4 * 1024**3, pending_gitlab_reserve_bytes=pending_gitlab_bytes,
                rule_reserve_bytes=int(1.125 * 1024**3), reserve_bytes=1024**3, passed=True)


def resources(root=ROOT, run_id=None):
    info = ro.inspect_pg(PG_CID)
    ro.guard_pg(info, OWNER)
    raw = subprocess.check_output(['docker', 'exec', PG_CID, 'cat', '/proc/meminfo'], text=True, timeout=5)
    available = int(re.search(r'^MemAvailable:\s+(\d+) kB', raw, re.M)[1]) * 1024
    vm = subprocess.check_output(['vm_stat'], text=True, timeout=5)
    page = int(re.search(r'page size of (\d+) bytes', vm)[1])
    pages = sum(int(re.search(r'^' + key + r':\s+(\d+)\.', vm, re.M)[1]) for key in ('Pages free', 'Pages inactive', 'Pages purgeable'))
    pressure = int(subprocess.check_output(['sysctl', '-n', 'kern.memorystatus_vm_pressure_level'], text=True, timeout=5).strip())
    pending, accounting = 4*1024**3, {'unavailable':True}
    result = subprocess.run(['docker','inspect',GITLAB_CID],capture_output=True,text=True,timeout=5)
    if result.returncode == 0:
        gitlab = json.loads(result.stdout)[0]
        cgroup = {}
        if gitlab.get('Id') == GITLAB_CID and gitlab.get('Image') == GITLAB_IMAGE and gitlab.get('State',{}).get('Running') is True and gitlab.get('Config',{}).get('Labels',{}).get('aiops.testlab.owner') == OWNER:
            capture = subprocess.run(['docker','exec',GITLAB_CID,'cat','/sys/fs/cgroup/memory.stat','/sys/fs/cgroup/memory.max','/sys/fs/cgroup/cpu.max'],capture_output=True,text=True,timeout=5)
            if capture.returncode == 0:
                lines = capture.stdout.splitlines()
                memory_stat = {k:int(v) for k,v in (line.split() for line in lines[:-2])}
                quota = lines[-1].split()
                cgroup = dict(anon=memory_stat.get('anon'),memory_max=int(lines[-2]),cpu_quota=int(quota[0])/int(quota[1]))
        pending, accounting = gitlab_remaining_reserve(gitlab,cgroup)
    observation = dict(observed_at=now(),vm_available_bytes=available,host_available_bytes=pages*page,host_pressure=pressure,
                       pending_gitlab_reserve_bytes=pending,parallel_gitlab_accounting=accounting,
                       rule_reserve_bytes=int(1.125*1024**3),reserve_bytes=1024**3,started_topology_API=False)
    folder=Path(root)/'.local/testlab/topology-protocol-parallel'
    folder.mkdir(mode=0o700,parents=True,exist_ok=True)
    ro._private(folder/('admission-'+(run_id or str(uuid.uuid4()))+'.json'),observation)
    return dict(resource_admission(available,pages*page,pressure,pending),parallel_gitlab_accounting=accounting)


def doctor(root, run_id=None):
    free_ports()
    active = active_runs(root, run_id)
    current = [row for row in active if row.get('current') is True]
    if len(current) != 1:
        raise Blocked('live topology package requires its exact public Runner parent/manifest')
    data = ro.doctor(root)
    pg = ro.inspect_pg(data['postgres_container'])
    runtime = Path(root) / '.local/testlab/runtime-config.json'
    check(data['source_tree_sha'] == SOURCE_SHA and data['binary_sha256'] == BINARY_SHA and
          pg['Id'] == PG_CID and data.get('runtime_sha') == RUNTIME_SHA and not runtime.is_symlink() and
          hashlib.sha256(runtime.read_bytes()).hexdigest() == RUNTIME_SHA, 'frozen source/binary/runtime/PG identity mismatch')
    return dict(data, postgres_container_id=pg['Id'], helper_sha256=hashes(), parallel_runs=active,
                formal_runner_run_id=current[0]['run_id'], resource_admission=resources(root,run_id))


def cleanup_clock(deadline):
    """Private imported module only: parent cleanup uses this same outer deadline."""
    class Clock:
        def __init__(self):
            self.first = True
        def monotonic(self):
            if self.first:
                self.first = False
                return deadline - 40
            return time.monotonic()
        def __getattr__(self, name):
            return getattr(time, name)
    return Clock()


class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        check(str(uuid.UUID(run_id)) == run_id and data.get('owner') == OWNER, 'canonical UUID/owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database, self.db_user = 'aiops_testlab_topology_' + self.nonce, 'topology_' + self.nonce
        self.marker, self.owner_tag = 'topologyfixture' + self.nonce, OWNER + ':' + run_id + ':topology-protocol'
        self.work = self.root / '.local/testlab/topology-protocol-parallel' / run_id
        check(self.work.resolve() == self.work, 'private UUID path required')
        self.work.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {n: uuid.uuid5(uuid.UUID(run_id), '20260930:' + n).hex for n in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {n: 'topology-' + self.nonce + '-' + n for n in self.users}
        self.cookies, self.processes, self.ids = {}, [], {}
        self.calls = 0
        self.role_attempted = self.db_attempted = False
        self.contract, self.protocol = Contract(self.root), Protocol(self)
        self.stage, self.config_hashes = 'bootstrap', {}
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
            owner_tag=self.owner_tag, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA,
            ports=list(RESERVED_PORTS), pids=[], started_at=now(), suite=MODULE,
            evidence_mode='protocol_mock', upstream_mode='protocol_mock', real_authority=False,
            no_external_notifications=True, no_model_inference=True, shared_components_untouched=True,
            formal_runner_run_id=data.get('formal_runner_run_id'))
        ro._private(self.work / 'state.json', self.state)
        ro._private(self.work / 'credentials.json', dict(password=self.password, database_password=self.db_password))

    def first_red(self, row):
        self.diagnostic(self.work / 'first-failure.json', row, exclusive=True)

    def frozen(self):
        super().frozen()
        check(hashes() == self.data['helper_sha256'] and hashlib.sha256((self.root / '.local/testlab/runtime-config.json').read_bytes()).hexdigest() == RUNTIME_SHA, 'helper/main runtime changed')
        for name, digest in self.config_hashes.items():
            path = self.work / name
            check(not path.is_symlink() and stat.S_IMODE(path.stat().st_mode) == 0o600 and hashlib.sha256(path.read_bytes()).hexdigest() == digest, 'invocation configuration changed')

    def pg(self, *args, **kwargs):
        if hasattr(self, 'cleanup_deadline'):
            kwargs['deadline'] = min(kwargs.get('deadline', self.cleanup_deadline), self.cleanup_deadline)
        info = ro.inspect_pg(self.data['postgres_container'], timeout=min(3, max(.05, kwargs.get('deadline', time.monotonic() + 3) - time.monotonic())))
        check(info['Id'] == PG_CID, 'exact owned PG CID required')
        return super().pg(*args, **kwargs)

    def call(self, user, method, path, *args, **kwargs):
        self.frozen()
        topology = method == 'GET' and urlsplit(path).path == '/api/v1/topology'
        upstream_mock = topology or method == 'POST' and bool(re.fullmatch(r'/api/v1/connectors/[^/]+/test',urlsplit(path).path))
        _thread.mode, _thread.call_id = ('protocol_mock' if upstream_mock else 'real_local'), self.calls + 1
        if topology and kwargs.get('scenario', 'normal') == 'normal' and 200 <= kwargs.get('want', 200) < 300:
            check(kwargs.get('validate') is not None, 'normal topology needs nonempty full DTO/source validator')
        try:
            return super().call(user, method, path, *args, **kwargs)
        finally:
            del _thread.mode, _thread.call_id

    def startup_environment(self):
        if self.stage == 'bootstrap':
            return {}
        return dict(AIOPS_INVENTORY_CONFIG=str(self.work / 'inventory.json'), AIOPS_V3_PROMETHEUS_QUERIES_CONFIG=str(self.work / 'queries.json'))

    def marker_case(self, name, actual):
        emit(name, 'passed', dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, **actual), evidence_mode='protocol_mock')

    def atomic_snapshot(self, body):
        path = self.work / ('snapshot-' + uuid.uuid4().hex + '.tmp')
        ro.private_file(path, json.dumps(body))
        os.replace(path, self.work / 'snapshot.json')

    def graph_read(self):
        q = "SELECT json_build_object('nodes',(SELECT json_agg(json_build_object('id',id,'external_id',external_id,'source_kind',source_kind,'source_id',source_id,'name',name)) FROM cmdb_resources WHERE source_id=" + ro.quote('topology-' + self.nonce) + "),'edges',(SELECT json_agg(json_build_object('id',id,'revision',to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),'kind',kind)) FROM cmdb_resource_relations WHERE source_id=" + ro.quote('topology-' + self.nonce) + '))'
        return json.loads(self.sql(q, read=True))

    def graph_refresh(self):
        raw = self.graph_read()
        check(len(raw.get('nodes') or []) == 2 and len(raw.get('edges') or []) == 1 and raw['edges'][0]['kind'] in ('calls','depends_on'), 'native exact graph absent')
        self.graph = {key: next(n for n in raw['nodes'] if n['external_id'] == self.nonce + '-' + key) for key in ('A', 'B')}
        self.graph['edge'] = raw['edges'][0]
        for key in ('A', 'B'):
            check(self.graph[key]['id'] == resource_id('docker', 'topology-' + self.nonce, self.nonce + '-' + key), 'native deterministic owned identity differs')
        ro._private(self.work / ('graph-' + uuid.uuid4().hex + '.json'), self.graph)

    def stop_apis(self):
        for p in self.processes:
            if p.poll() is None:
                p.terminate()
                p.wait(timeout=8)
        check(all(p.poll() is not None for p in self.processes), 'bootstrap API must exit before controlled restart')

    def grant(self, user, module, present):
        values = (self.roles[user], module, 'view', 'application', self.app)
        if module in ('environment', 'connectors'):
            values = (self.roles[user], module, 'view', 'environment', 'test')
        condition = ' AND '.join(key + '=' + ro.quote(value) for key, value in zip(('role_id','module','action','scope_kind','scope_id'), values))
        if present:
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' + ','.join(map(ro.quote, values)) + ') ON CONFLICT DO NOTHING;')
        else:
            self.sql('DELETE FROM v3_role_grants WHERE ' + condition + ';')

    def inflight_fault(self, mode):
        if mode == 'revoke_grant':
            self.grant('reader', 'resources', False)
        elif mode == 'revoke_session':
            session = self.cookies['reader'].split('=', 1)[1]
            self.sql('DELETE FROM sessions WHERE token_hash=' + ro.quote(hashlib.sha256(session.encode()).hexdigest()) + ';')
        else:
            value = copy.deepcopy(self.source_snapshot)
            observed = now()
            value['sources'][0]['observed_at'] = observed
            for n in value['sources'][0]['resources']:
                n['observed_at'] = observed
            value['sources'][0]['relations'][0]['kind'] = 'depends_on'
            self.atomic_snapshot(value)
            deadline = time.monotonic() + 3.5
            while time.monotonic() < deadline:
                raw = self.graph_read()
                if any(e['kind'] == 'depends_on' and e['id'] != self.graph['edge']['id'] for e in raw.get('edges') or []):
                    self.source_snapshot = value
                    return
                time.sleep(.05)
            raise Blocked('native importer did not change graph within bounded inflight window')

    def run(self):
        self.provision()
        self.protocol.start()
        self.start()
        self.login('admin')
        name = 'topology-' + self.nonce
        self.call('admin', 'POST', '/api/v1/services', {'items':[{'id':name,'tier':'T1'}]}, want=201, scenario='fixture',
                  validate=lambda v: check(v == {'created':1}, 'public service producer mismatch'))
        self.app = self.sql('SELECT id FROM applications WHERE key=' + ro.quote(name), read=True)
        env_id = self.sql('SELECT id FROM application_environments WHERE application_id=' + ro.quote(self.app) + " AND key='test'", read=True)
        value, _ = self.call('admin', 'POST', '/api/v1/connectors', dict(type='prometheus', url='http://127.0.0.1:' + str(METRICS_PORT), auth='none', tools=['query_metrics'], webhook=False), want=201, scenario='fixture', validate=lambda v: check(bool(v.get('id')), 'public connector ID missing'))
        self.connector = value['id']
        self.call('admin', 'POST', '/api/v1/connectors/' + self.connector + '/test', scenario='fixture')
        self.sid = base64.urlsafe_b64encode((self.app + '\0test').encode()).decode().rstrip('=')
        self.call('admin', 'POST', '/api/v1/services/' + self.sid + '/external-targets', dict(connector_id=self.connector, target_kind='metric_resource', external_id='topology-' + self.nonce), want=201, scenario='fixture')
        self.seed()
        self.sql('DELETE FROM v3_role_grants WHERE role_id=ANY(ARRAY[' + ','.join(map(ro.quote,self.roles.values())) + "]) AND module<>'environment';")
        for user in ('reader', 'peer'):
            for module in ('resources', 'services', 'connectors'):
                self.grant(user, module, True)
        for user in self.users:
            self.login(user)
        self.audit_before = self.sql('SELECT COALESCE(json_agg(to_jsonb(a) ORDER BY id),\'[]\'::json) FROM management_audit a', read=True)
        self.stop_apis()
        for index in range(2):
            os.rename(self.work / ('api-' + str(index) + '.log'), self.work / ('api-bootstrap-' + str(index) + '.log'))
        self.expression = 'topology_protocol{application_id="' + self.app + '",environment_id="test"}'
        self.source_snapshot = snapshot(self.nonce, self.app, env_id, now())
        self.atomic_snapshot(self.source_snapshot)
        ro._private(self.work / 'inventory.json', dict(interval_seconds=10, sources=[], external_snapshot_file=str(self.work / 'snapshot.json')))
        ro._private(self.work / 'queries.json', dict(queries=[dict(connector_id=self.connector, application_id=self.app, environment_id='test', resource_id='topology-' + self.nonce, query_id='topology_v1', expression=self.expression)]))
        self.config_hashes = {name:hashlib.sha256((self.work/name).read_bytes()).hexdigest() for name in ('inventory.json','queries.json')}
        self.stage = 'full'
        self.start()
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if len(self.graph_read().get('nodes') or []) == 2:
                break
            time.sleep(.1)
        self.graph_refresh()
        self.path = '/api/v1/topology?' + urlencode(dict(around=self.graph['B']['id'], depth=1, group_by='cluster'))
        self.marker_case('topology.native_import', dict(nodes=2, edges=1, producer='external_snapshot_file→native importer→SaveSnapshot', SQL_graph_writes=0, actual_authority=False))
        self.normal()
        self.marker_case('topology.normal', dict(full_DTO=True, nodes=2, edges=1, matrix_series=11, samples_per_series=6))
        before = len(self.protocol.records)
        for user in (None, 'denied', 'hidden'):
            self.call(user, 'GET', self.path, want=401 if user is None else 404, scenario='authorization', environment='staging' if user == 'hidden' else 'test')
        check(len(self.protocol.records) == before, 'hidden/unauthenticated graph contacted metrics')
        self.marker_case('topology.authorization', dict(unauthenticated=401, hidden=404, no_upstream=True))
        for query in ('', '?around=x&unknown=x', '?around=x&depth=4', '?around=x&depth=1&depth=1'):
            self.call('reader','GET','/api/v1/topology'+query,want=422,code='invalid_topology_query',scenario='boundary')
        self.call('reader','GET',self.path,want=422,code='invalid_topology_query',scenario='boundary',environment='all')
        self.marker_case('topology.boundary', dict(single_environment=True, duplicates_unknown_depth_rejected=True))
        for mode in ('503','warning','identity','cross_scope','missing','grid','revision','zero_rps','labels','duplicate','bounds'):
            self.protocol.mode = mode
            self.call('reader','GET',self.path,want=503,scenario='failure')
            self.protocol.mode = 'normal'
            self.normal()
        self.marker_case('topology.source_faults', dict(faults=11, recoveries=11, no_placeholder200=True))
        self.grant('reader','services',False)
        before = len(self.protocol.records)
        # resolveServiceMetricTarget hides forbidden candidates; no visible source
        # maps to the existing full-view 503, never a successful or leaked graph.
        self.call('reader','GET',self.path,want=503,code='topology_source_incomplete',scenario='authorization')
        check(len(self.protocol.records) == before, 'revoked service grant reached upstream metrics')
        self.grant('reader','services',True)
        self.normal()
        self.protocol.mode='revoke_grant'
        self.call('reader','GET',self.path,want=404,code='topology_center_not_found',scenario='authorization')
        self.protocol.mode='normal'
        self.grant('reader','resources',True)
        self.normal()
        self.marker_case('topology.grant_revocation',dict(current_service_and_inflight_resource_checked=True))
        # Align to the next normal native import tick; no clock or TTL alteration.
        deadline=time.monotonic()+14
        while time.monotonic()<deadline:
            age=float(self.sql("SELECT extract(epoch FROM now()-last_attempt_at) FROM cmdb_sync_sources WHERE id="+ro.quote('topology-'+self.nonce),read=True))
            if 8.0 <= age < 9.5:
                break
            time.sleep(.1)
        else:
            raise Blocked('native import stable-window scheduling unavailable')
        self.protocol.mode='graph_change'
        self.call('reader','GET',self.path,want=503,code='topology_source_incomplete',scenario='failure')
        self.protocol.mode='normal'
        check(any(e['kind']=='depends_on' and e['id']!=self.graph['edge']['id'] for e in self.graph_read()['edges']), 'native relation identity/revision mutation not evidenced')
        self.graph_refresh()
        self.normal()
        self.marker_case('topology.graph_revision',dict(native_graph_changed_during_IO=True, relation_revision= self.graph['edge']['revision'], SQL_graph_writes=0))
        self.protocol.mode='revoke_session'
        self.call('reader','GET',self.path,want=403,scenario='authorization')
        self.protocol.mode='normal'
        self.login('reader')
        self.normal()
        self.marker_case('topology.session_revocation',dict(inflight_session_rechecked=True, recovered_with_new_login=True))
        audit_after=self.sql('SELECT COALESCE(json_agg(to_jsonb(a) ORDER BY id),\'[]\'::json) FROM management_audit a',read=True)
        check(audit_after==self.audit_before,'topology read/rejection mutated management audit')
        self.marker_case('topology.audit',dict(read_and_rejection_audit_delta=0, public_producer_audit_preserved=True))
        self.frozen()
        result=dict(required_cases=list(REQUIRED),protocol_mock_normal_targets=['GET /api/v1/topology'],real_normal_targets=[],
                    real_authority_complete=False,actual_call_graph_verified=False,performance_or_soak=False,http_results=self.calls)
        ro._private(self.work/'result.json',result)
        return result

    def normal(self):
        for index in (0,1):
            self.call('reader','GET',self.path,index=index,validate=lambda v:validate_view(v,self.graph))

    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40
        self.protocol.release.set()
        primary=None
        original=ro.time
        ro.time=cleanup_clock(self.cleanup_deadline)
        try:
            super().cleanup()
        except BaseException as error:
            primary=error
        finally:
            ro.time=original
        try:
            self.protocol.close(self.cleanup_deadline)
            free_ports()
            check(time.monotonic()<=self.cleanup_deadline,'single 40-second cleanup exceeded')
            ro._private(self.work/'protocol-cleanup.json',dict(held_servers_closed=True,ports_free=list(RESERVED_PORTS),single_budget_seconds=40))
        except BaseException as error:
            primary=primary or error
        if primary is not None:
            raise primary


def qualified(rows, interfaces, run_id, required_complete, cleanup_complete):
    reasons=[]
    if not required_complete or not cleanup_complete or any(r.get('status')!='passed' for r in rows):
        reasons.append('required scenarios/cleanup missing or failed/interrupted case')
    proofs=[]
    if not reasons:
        if len({r.get('call_id') for r in interfaces}) != len(interfaces):
            reasons.append('duplicate persistent HTTP call identity')
        receipts={r.get('call_id'):r for r in interfaces}
        for row in rows:
            cell=row.get('interface',{})
            if cell.get('method')!='GET' or cell.get('path')!='/api/v1/topology' or cell.get('scenario')!='normal':
                continue
            if (row.get('status')==cell.get('status')=='passed' and all(r.get('evidence_mode')==r.get('upstream_mode')=='protocol_mock' for r in (row,cell)) and
                cell.get('scenario_detail')=='normal' and cell.get('route_class')=='main' and cell.get('registration_evidence') is True and
                type(cell.get('actual',{}).get('status')) is int and cell['actual']['status']==200 and cell.get('run_id')==run_id and
                re.fullmatch(re.escape(run_id)+r'-[1-9][0-9]*',cell.get('call_id','')) and receipts.get(cell['call_id'])==cell):
                proofs.append(cell['call_id'])
            else:
                reasons.append('normal protocol scope/receipt identity mismatch')
    if not proofs:
        reasons.append('formal normal topology proof missing')
    return dict(ready=not reasons,rejection_reasons=reasons,normal_http_proofs={'real_local':{},'protocol_mock':{'GET /api/v1/topology':proofs} if not reasons else {}})


def handoff(chain,status,error):
    def read(name,lines=False):
        try:
            path=chain.work/name
            check(not path.is_symlink() and stat.S_IMODE(path.stat().st_mode)==0o600,'private evidence required')
            raw=path.read_text()
            return [json.loads(x) for x in raw.splitlines()] if lines else json.loads(raw)
        except (OSError,ValueError,AssertionError):
            return None
    rows=read('cases.jsonl',True) or []
    result,cleanup,protocols=read('result.json'),read('cleanup.json'),read('protocol-cleanup.json')
    final=ro.source_fingerprint(chain.root)
    required=bool(result and result.get('required_cases')==list(REQUIRED) and set(REQUIRED).issubset({r['name'] for r in rows if r['status']=='passed'}) and final==SOURCE_SHA and status=='passed')
    clean=bool(cleanup and all(cleanup.get(k) is True for k in ('database_absent','role_absent','owned_processes_exited')) and protocols and protocols.get('ports_free')==list(RESERVED_PORTS) and protocols.get('held_servers_closed') is True)
    qualification=qualified(rows,read('interfaces.jsonl',True) or [],chain.run_id,required,clean)
    evidence=dict(run_id=chain.run_id,status=status,module=MODULE,source_tree_sha=SOURCE_SHA,source_tree_sha_end=final,
        formal_runner_run_id=chain.data.get('formal_runner_run_id'),formal_runner_status_pending=True,
        formal_runner_pass_required_before_coverage=True,
        binary_sha256=BINARY_SHA,main_runtime_sha=RUNTIME_SHA,helper_sha256=chain.data['helper_sha256'],
        started_at=chain.state['started_at'],ended_at=now(),qualification=qualification,
        case_counts={k:sum(r['status']==k for r in rows) for k in ('passed','failed','blocked','interrupted')},
        protocol_mock_normal_targets=list(qualification['normal_http_proofs']['protocol_mock']),real_normal_targets=[],
        real_authority_complete=False,remaining='Authoritative call graph and traffic producer, real source completeness, performance and24h remain unverified',
        artifacts={'cleanup':str(chain.work/'cleanup.json'),'first_red':str(chain.work/'first-failure.json'),'raw_http':str(chain.work),'source_reads':str(chain.work/'source-reads.jsonl')},
        error_type=type(error).__name__ if error else None)
    ro._private(chain.work/'handoff.json',evidence)
    ro.private_file(chain.work/'handoff.md','# Topology protocol package\n\n'+json.dumps(evidence,ensure_ascii=False,indent=2)+'\n')
    return evidence


def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,default=ROOT)
    p.add_argument('--run-id')
    m=p.add_mutually_exclusive_group(required=True)
    m.add_argument('--inventory',action='store_true')
    m.add_argument('--live',action='store_true')
    a=p.parse_args(argv)
    if a.inventory:
        print(json.dumps(dict(module=MODULE,targets=TARGETS,support=SUPPORT,ports=RESERVED_PORTS,offline_only=True,evidence_mode='protocol_mock')))
        return 0
    global ACTIVE
    chain,error,status,report=None,None,'failed',None
    try:
        with exclusive(a.root),ro.cooperative_signals():
            run_id=a.run_id or str(uuid.uuid4())
            data=doctor(a.root,run_id)
            chain=Chain(a.root,data,run_id)
            ACTIVE=chain
            ro._private(chain.work/'doctor-before.json',{k:data[k] for k in ('owner','source_tree_sha','binary_sha256','runtime_sha','postgres_container_id','helper_sha256','parallel_runs','resource_admission')})
            ro.execute(chain)
            status='passed'
    except BaseException as failure:
        error=failure
        status='interrupted' if isinstance(failure,(KeyboardInterrupt,SystemExit)) else 'blocked' if isinstance(failure,Blocked) else 'failed'
        emit('topology.package_error',status,dict(error_type=type(failure).__name__,reason=str(failure)))
    finally:
        if chain is not None:
            report=handoff(chain,status,error)
            emit('topology.handoff',status,dict(run_id=chain.run_id,handoff=str(chain.work/'handoff.json'),counts=report['case_counts'],qualification=report['qualification']))
        ACTIVE=None
    # Explicit blocked cases make the public Runner blocked, never green.
    return 0 if status=='blocked' or status=='passed' and report and report['qualification']['ready'] else 1


if __name__=='__main__':
    raise SystemExit(main())

"""Six bounded Pull requests: four actual pending reads, reject fifth, recover sixth.

Prepare/offline-safe on import. --live requires fresh root admission and the sole
matching Runner. Exposition is synthetic; Prometheus scrapes/queries are real.
The relay changes timing only. No performance, Push, model, Vault or full chain.
"""
from __future__ import annotations
import argparse, base64, collections, fcntl, hashlib, http.client, importlib.util
import datetime, inspect, json, math, os, re, socket, sqlite3, stat, subprocess, textwrap, threading, time, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PROM_IMAGE = 'sha256:249c7fa94198524d75d26cae28169774e41627c2d0fc1b7104f2feec8d192d0f'
PORTS = (18824, 18825)
PROM_PORT, EXPORT_PORT, RELAY_PORT = 18826, 18827, 18828
ALL_PORTS = (*PORTS, PROM_PORT, EXPORT_PORT, RELAY_PORT)
MODULE = 'formal-api-pull-pending-capacity'
HOLD_SECONDS = 3.0  # Always below the existing provider's five-second deadline.
REQUIRED_CASES = tuple('pull_capacity.'+s for s in
    ('source', 'pending_four', 'fifth_rejected', 'no_rejected_io', 'release_recovery', 'audit_consistency', 'cleanup'))

_spec = importlib.util.spec_from_file_location('pull_capacity_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote

# Keep the original cleanup implementation, changing only its local deadline
# in this private module copy. DB/Popen and source cleanup share ONE deadline.
_cleanup_source = inspect.getsource(ro.Chain.cleanup)
_cleanup_source = textwrap.dedent(_cleanup_source)
_needle = 'deadline = time.monotonic() + 40'
check(_cleanup_source.count(_needle) == 1, 'readonly cleanup deadline contract changed')
_cleanup_globals = dict(ro.__dict__)
exec(compile(_cleanup_source.replace(_needle, 'deadline = self.cleanup_deadline'),
             '<private-pull-capacity-cleanup>', 'exec'), _cleanup_globals)
_readonly_cleanup = _cleanup_globals['cleanup']
_inspect_pg = ro.inspect_pg
def exact_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs)
    check(info.get('Id') == PG_CID, 'exact owned PG CID differs')
    return info
ro.inspect_pg = exact_pg

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields)), flush=True)
ro.emit = emit

def hashes():
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_pull_pending_capacity_coverage.py', 'api_readonly_chain.py',
                         'runner.py', 'store.py', 'env.py', 'backend.py', 'faults.py',
                         'identity.py', 'inventory.py', 'components.py',
                         'tests/test_api_pull_pending_capacity_coverage.py')}

def private_json(path, value):
    raw = json.dumps(value, ensure_ascii=False).encode()
    check(len(raw) <= 131072, 'bounded private evidence exceeded 128KiB')
    ro._private(path, value)

def explicit_absence(result, name):
    allowed = {'Error: No such object: '+name, 'Error: No such container: '+name,
               'Error response from daemon: No such container: '+name,
               'error: no such object: '+name}
    if result.returncode != 1 or result.stdout.strip() not in ('', '[]') or result.stderr.strip() not in allowed:
        raise Blocked('exact planned UUID container absence unconfirmed')

def inspect_container(target, work, phase, timeout=3, receipt_errors=None):
    if receipt_errors is not None:
        check(phase in ('cleanup-before', 'cleanup-after') and isinstance(receipt_errors, list),
              'receipt failure continuation restricted to cleanup inspection')
    result = subprocess.run(['docker', 'inspect', target], capture_output=True, text=True, timeout=timeout)
    try:
        private_json(work/('container-inspect-'+phase+'.json'), dict(target=target, phase=phase,
            returncode=result.returncode, stdout=result.stdout[:32768], stderr=result.stderr[:32768]))
    except BaseException as error:
        if receipt_errors is None: raise
        receipt_errors.append(error)  # Keep final failure; do not strand an attributable owned resource.
    if result.returncode:
        explicit_absence(result, target); return None
    rows = json.loads(result.stdout)
    check(isinstance(rows, list) and len(rows) == 1, 'ambiguous owned container inspection')
    return rows[0]

def guard_prom(info, chain, running=True):
    host, config = info.get('HostConfig', {}), info.get('Config', {})
    check(info.get('Name') == '/'+chain.prom_name and info.get('Image') == PROM_IMAGE
          and re.fullmatch('[a-f0-9]{64}', info.get('Id', ''))
          and (not chain.prom_cid or info['Id'] == chain.prom_cid)
          and all(config.get('Labels', {}).get(k) == v for k,v in chain.labels.items()), 'UUID Prom identity differs')
    check(host.get('NanoCpus') == 500000000 and host.get('Memory') == 268435456
          and host.get('MemorySwap') == 268435456 and host.get('Privileged') is False
          and host.get('ReadonlyRootfs') is True and host.get('CapDrop') == ['ALL']
          and host.get('SecurityOpt') == ['no-new-privileges']
          and host.get('Tmpfs') == {'/prometheus':chain.tmpfs}
          and config.get('User') == str(os.getuid())+':'+str(os.getgid()) and os.getuid() != 0,
          'UUID Prom resource/security boundary differs')
    ports = info.get('NetworkSettings', {}).get('Ports', {}) if info.get('State', {}).get('Running') else host.get('PortBindings', {})
    check(ports.get('9090/tcp') == [dict(HostIp='127.0.0.1', HostPort=str(PROM_PORT))]
          and all(k == '9090/tcp' or v is None for k,v in ports.items()), 'Prom publishing escaped loopback')
    binds = [m for m in info.get('Mounts', []) if m.get('Type') == 'bind']
    check(len(binds) == 1 and binds[0].get('Source') == str(chain.work/'prometheus.json')
          and binds[0].get('Destination') == '/etc/prometheus/owned.json' and binds[0].get('RW') is False
          and not any(m.get('Type') == 'volume' for m in info.get('Mounts', []))
          and info.get('Path') == '/bin/prometheus' and info.get('Args') == chain.prom_args,
          'Prom readonly config/storage/argv differs')
    if running: check(info.get('State', {}).get('Running') is True, 'owned Prom not running')
    return info['Id']

def admission(path, run_id):
    path = Path(path)
    check(path.is_absolute() and path.resolve() == path and path.is_relative_to(ROOT/'.local/testlab'), 'private admission path required')
    st = path.lstat()
    check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode) == 0o600 and st.st_uid == os.getuid(), 'own regular0600 admission required')
    check(st.st_size <= 65536, 'admission too large')
    data = json.loads(path.read_text())
    expected = dict(status='root-admitted-pull-pending-capacity', resource_run_id=run_id, owner=ro.OWNER,
        source_tree_sha=SOURCE, binary_sha256=BINARY, runtime_sha256=RUNTIME, postgres_cid=PG_CID,
        prometheus_image_id=PROM_IMAGE, ports=list(ALL_PORTS), helper_sha256=hashes())
    check(all(data.get(k) == v for k,v in expected.items()), 'exact root capacity admission differs')
    for key in ('fresh_nonce_hmac_process_tuple_verified', 'headroom_verified', 'owned_container_cleanup_authorized'):
        check(data.get(key) is True, 'root capacity prerequisite missing: '+key)
    db = sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro', uri=True)
    try: active = [json.loads(r[0]) for r in db.execute('SELECT data FROM runs') if json.loads(r[0]).get('status') == 'running']
    finally: db.close()
    check(len(active) == 1 and active[0].get('selected_suite') == MODULE
          and re.fullmatch('[a-f0-9]{64}', str(data.get('manifest_sha256', '')))
          and active[0].get('manifest_sha') == data['manifest_sha256']
          and active[0].get('source_tree_sha') == SOURCE and active[0].get('pid') == os.getppid(),
          'sole matching actual Runner required')
    return data

def doctor():
    data = ro.doctor(ROOT)
    check(data['source_tree_sha'] == SOURCE and data['binary_sha256'] == BINARY and data['runtime_sha'] == RUNTIME,
          'source/nonrace binary/runtime differs')
    info = ro.inspect_pg(data['postgres_container'])
    check(info.get('Id') == PG_CID and info.get('HostConfig', {}).get('NanoCpus') == 4_000_000_000
          and info.get('HostConfig', {}).get('Memory') == 2 << 30, 'exact owned PG4CPU2GiB required')
    images = json.loads(subprocess.check_output(['docker', 'image', 'inspect', PROM_IMAGE], text=True, timeout=5))
    check(len(images) == 1 and images[0].get('Id') == PROM_IMAGE, 'immutable local Prom image missing; no pull/build')
    for port in ALL_PORTS:
        with socket.socket() as sock: check(sock.connect_ex(('127.0.0.1', port)) != 0, 'capacity port occupied')
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        p = urlsplit(path)
        check(not (p.scheme or p.netloc or p.query or p.fragment), 'exact relative formal path required')
        canonical, kind = super().canonical(method, path)
        allowed = {('POST','/auth/login'), ('POST','/services'), ('POST','/connectors'),
                   ('POST','/services/{id}/external-targets'), ('GET','/ingest/pull/{id}')}
        check((method, canonical[7:]) in allowed, 'outside six-request capacity inventory')
        return canonical, kind

class Gate:
    """Read the real upstream immediately, hold its unchanged response briefly."""
    def __init__(self, expression, upstream, clock=time.monotonic):
        self.expression, self.upstream, self.clock = expression, upstream, clock
        self.condition = threading.Condition(); self.release = threading.Event()
        self.arrivals = []; self.receipts = {}; self.first = None; self.expired = False

    def validate(self, path, authorization=None):
        p = urlsplit(path); query = parse_qs(p.query, keep_blank_values=True)
        check(not (p.scheme or p.netloc or p.fragment) and p.path == '/api/v1/query_range'
              and not authorization and set(query) == {'query','start','end','step','timeout','limit'}
              and all(len(v) == 1 for v in query.values()) and query['query'] == [self.expression]
              and query['step'] == ['30'] and query['timeout'] == ['5s'] and query['limit'] == ['100'],
              'relay escaped exact registered source/deadline')

    def read(self, path, authorization=None):
        self.validate(path, authorization)
        with self.condition:
            index = len(self.arrivals)
            if not self.release.is_set(): check(index < 4, 'fifth rejected call reached source')
            if self.first is None: self.first = self.clock()
            self.arrivals.append(dict(index=index, path=path, arrived=self.clock()))
            self.condition.notify_all()
        status, raw, media_type = self.upstream(path)
        check(isinstance(raw, bytes) and len(raw) <= 65536, 'source response size invalid')
        with self.condition:
            self.receipts[index] = dict(status=status, raw=raw, media_type=media_type,
                sha256=hashlib.sha256(raw).hexdigest(), path=path)
            self.condition.notify_all()
        if not self.release.wait(max(0, self.first+HOLD_SECONDS-self.clock())):
            self.expired = True; self.release.set()
        return status, raw, media_type

    def wait_four(self):
        initial = self.clock()+HOLD_SECONDS
        with self.condition:
            while len(self.arrivals) < 4 or len(self.receipts) < 4:
                left = min(initial, (self.first+HOLD_SECONDS) if self.first is not None else initial)-self.clock()
                if left <= 0 or self.expired: raise Blocked('four real arrivals not observable within provider window; no retry')
                self.condition.wait(min(.05, left))
        if self.expired or self.clock() >= self.first+HOLD_SECONDS:
            raise Blocked('source hold window expired; no retry')
        return self.first+HOLD_SECONDS

class OwnedServer(ThreadingHTTPServer):
    daemon_threads = False
    block_on_close = False
    def __init__(self, port, handler):
        self.children = set(); self.child_lock = threading.Lock()
        super().__init__(('127.0.0.1', port), handler)
        self.thread = threading.Thread(target=self.serve_forever, kwargs={'poll_interval':.05}, daemon=False)
        self.thread.start()
    def process_request_thread(self, *args):
        with self.child_lock: self.children.add(threading.current_thread())
        try: super().process_request_thread(*args)
        finally:
            with self.child_lock: self.children.discard(threading.current_thread())
    def close(self, deadline):
        self.shutdown(); self.server_close(); self.thread.join(max(0, deadline-time.monotonic()))
        with self.child_lock: children = list(self.children)
        for child in children: child.join(max(0, deadline-time.monotonic()))
        check(not self.thread.is_alive() and all(not c.is_alive() for c in children), 'own source threads remain')
    def get_request(self):
        request, address = super().get_request(); request.settimeout(2)
        return request, address

def pending_four(ledger, actors, source):
    rows = ledger['attempts']
    check(len(rows) == 4 and collections.Counter(x['actor_id'] for x in rows) == collections.Counter(actors)
          and all(x['source_id'] == source and x['kind'] == 'preview' and x['status'] == 'pending' for x in rows)
          and ledger['audit'] == [], 'four actual authorized pending rows not observed')

def check_completed(ledger, actors, source, application, counts):
    rows, audits = ledger['attempts'], ledger['audit']
    check(len(rows) == len(actors) == len(audits) and len({x['id'] for x in rows}) == len(rows)
          and collections.Counter(x['actor_id'] for x in rows) == collections.Counter(actors), 'attempt actor/count/unique identity differs')
    by_actor = collections.defaultdict(list)
    for row in rows:
        check(row['source_id'] == source and row['kind'] == 'preview' and row['status'] == 'success'
              and row['rows_count'] > 0 and row['dropped_count'] == 0 and row['error_code'] == '', 'attempt partial/failure state remains')
        by_actor[row['actor_id']].append(row['rows_count'])
    check({k:sorted(v) for k,v in by_actor.items()} == {k:sorted(v) for k,v in counts.items()}, 'attempt rows differ from original real source')
    check(collections.Counter(a['actor_id'] for a in audits) == collections.Counter(actors), 'audit actor cardinality differs')
    for a in audits:
        change = a['changes']
        check(a['application_id'] == application and a['action'] == 'v3.ingest_pull.preview'
              and a['target'] == source and a['outcome'] == 'success'
              and set(change) == {'source_id','environment_id','kind','rows','dropped','error_code'}
              and change['source_id'] == source and change['environment_id'] == 'test' and change['kind'] == 'preview'
              and change['rows'] in counts[a['actor_id']] and change['dropped'] == 0 and change['error_code'] == '', 'exact audit linkage differs')
    check(collections.Counter((a['actor_id'], a['changes']['rows']) for a in audits)
          == collections.Counter((actor, count) for actor, values in counts.items() for count in values), 'audit row cardinality differs')

def validate_dto(value, receipt, chain):
    check(receipt['status'] == 200, 'actual Prom query not successful')
    raw = json.loads(receipt['raw']); series = raw.get('data', {}).get('result')
    check(raw.get('status') == 'success' and raw.get('data', {}).get('resultType') == 'matrix'
          and isinstance(series, list) and len(series) == 1, 'real Prom matrix not exact own series')
    item = series[0]; labels = item.get('metric', {})
    check(labels.get('signal_id') == chain.nonce and labels.get('application') == chain.app_key
          and labels.get('environment') == 'test' and labels.get('service') == 'pull-capacity', 'real matrix scope differs')
    points = item.get('values', [])
    check(bool(points) and all(len(v) == 2 and math.isfinite(float(v[0])) and float(v[1]) == 7 for v in points), 'own scraped scalar/sample population differs')
    rows = value.get('preview', {}).get('rows', [])
    check(value.get('id') == chain.source_id and value.get('contract') == 'metric' and value.get('contract_version') == 'MetricSeries v1'
          and value.get('step') == 3 and value['request']['method'] == 'GET' and value['request']['host'] == chain.relay_url
          and value['request']['path'] == '/api/v1/query_range' and value['sample']['status'] == 200
          and len(rows) == min(20, len(points)), 'exact Pull DTO/request/sample cardinality differs')
    expected_rows = []
    for timestamp, number in points[:20]:
        check(float(timestamp) == int(float(timestamp)), 'registered whole-second sample contract differs')
        stamp = datetime.datetime.fromtimestamp(float(timestamp),datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
        expected_rows.append(dict(service=chain.app,name='pull-capacity',time=stamp,value=number,labels={'signal_id':chain.nonce}))
    check(rows == expected_rows, 'normalized actual timestamp/value/scope differs from real matrix')
    check(json.loads(value['sample']['body']) == rows[:5] and chain.expression not in json.dumps(value)
          and {c['label'] for c in value['preview']['checks']} ==
          {str(len(points))+' converted rows','0 dropped','Complete bounded response'}, 'projection count/redaction differs')
    return len(points)

class Chain(ro.Chain):
    def __init__(self, data, run_id):
        super().__init__(ROOT, data, run_id)
        self.contract = Contract(ROOT); self.generation = 0; self.configured = False
        self.evidence_work = self.work
        self.prom_cid = None; self.prom_attempted = False; self.servers = []; self.workers = []; self.responses = {}
        self.gate = None; self.cleanup_deadline = None; self.config_hashes = {}; self.admin_actor = None
        self.prom_name = 'aiops-'+ro.OWNER+'-pull-capacity-'+self.nonce
        self.labels = {'aiops.testlab.owner':ro.OWNER,'aiops.testlab.run':run_id,'aiops.testlab.purpose':'pull-capacity'}
        self.tmpfs = 'rw,noexec,nosuid,size=33554432,uid='+str(os.getuid())+',gid='+str(os.getgid())+',mode=0700'
        self.prom_args = ['--config.file=/etc/prometheus/owned.json','--storage.tsdb.path=/prometheus',
                          '--storage.tsdb.retention.time=1h','--web.listen-address=0.0.0.0:9090']
        self.source_id = 'pull-capacity-'+self.nonce; self.resource_id = 'capacity-'+self.nonce
        self.app_key = 'capacity-'+self.nonce; self.relay_url = 'http://127.0.0.1:'+str(RELAY_PORT)
        self.expression = 'testlab_requests_total{application="'+self.app_key+'",environment="test",signal_id="'+self.nonce+'"}'
        self.state.update(suite=MODULE, ports=list(ALL_PORTS), model_scope='inert readonly configuration; no invocation',
            normal_routes=[], source_scope='own UUID exposition and real Prom; timing relay only', source_configured=False)
        private_json(self.work/'state.json', self.state)

    def frozen(self):
        check(hashes() == self.data['helper_sha256'], 'capacity helper/runner dependency changed')
        check(hashlib.sha256((ROOT/'.local/testlab/runtime-config.json').read_bytes()).hexdigest() == RUNTIME, 'runtime changed')
        for name, sha in self.config_hashes.items():
            path = self.evidence_work/name
            check(not path.is_symlink() and stat.S_IMODE(path.stat().st_mode) == 0o600
                  and hashlib.sha256(path.read_bytes()).hexdigest() == sha, 'own source config drift')
        super().frozen()

    def marker_case(self, suffix, actual):
        name = 'pull_capacity.'+suffix; check(name in REQUIRED_CASES, 'unknown capacity marker')
        self.frozen(); emit(name, 'passed', dict(run_id=self.run_id, source_tree_sha=SOURCE, **actual))

    def startup_environment(self):
        return {'AIOPS_V3_PULL_SOURCES_CONFIG':str(self.work/'pull.json')} if self.configured else {}

    def start(self):
        # A new private log/config folder for each generation; only own Popen
        # handles are stopped and all remain reachable for final cleanup.
        generation = self.generation
        folder = self.work/('generation-'+str(generation)); self.generation += 1; folder.mkdir(mode=0o700)
        for name in ('instance.json','models.json','commands.json'): ro.private_file(folder/name, (self.work/name).read_text())
        saved = self.work; self.work = folder
        if self.configured: ro.private_file(folder/'pull.json', (saved/'pull.json').read_text())
        original_emit = ro.emit
        def generation_emit(name, status, actual, **fields):
            if name in ('readonly.api_ready.0', 'readonly.api_ready.1'):
                name = 'pull_capacity.generation.'+str(generation)+'.api_ready.'+name.rsplit('.',1)[1]
            original_emit(name, status, actual, **fields)
        ro.emit = generation_emit  # Only this private readonly module's startup results.
        try: super().start()
        finally:
            ro.emit = original_emit
            self.work = saved; private_json(saved/'state.json',self.state)

    def stop_generation(self):
        current = self.processes[-2:]; check(len(current) == 2, 'two exact generation handles required')
        for proc, port in zip(current, PORTS):
            if proc.poll() is None: check(ro._process_port(proc.pid, port), 'API ownership changed')
        for proc in current:
            if proc.poll() is None:
                proc.terminate()
                try: proc.wait(timeout=8)
                except subprocess.TimeoutExpired: proc.kill(); proc.wait(timeout=2)

    def prom_get(self, path):
        p = urlsplit(path)
        check(not (p.scheme or p.netloc or p.fragment) and p.path in ('/api/v1/query','/api/v1/query_range','/-/ready'), 'owned Prom path escaped')
        conn = http.client.HTTPConnection('127.0.0.1', PROM_PORT, timeout=1)
        try:
            conn.request('GET', path); response = conn.getresponse(); raw = response.read(65537)
            check(len(raw) <= 65536, 'Prom evidence exceeds bound')
            return response.status, raw, response.getheader('Content-Type', 'application/json')
        finally: conn.close()

    def start_source(self):
        chain = self
        metric = ('# TYPE testlab_requests_total counter\ntestlab_requests_total{application="'+self.app_key+
                  '",environment="test",signal_id="'+self.nonce+'",service="pull-capacity"} 7\n').encode()
        class Exporter(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_GET(self):
                if self.path != '/metrics': self.send_error(404); return
                self.send_response(200); self.send_header('Content-Type','text/plain; version=0.0.4')
                self.send_header('Content-Length',str(len(metric))); self.end_headers(); self.wfile.write(metric)
        with ro.acquisition_signals():
            self.servers.append(OwnedServer(EXPORT_PORT, Exporter))
        self.gate = Gate(self.expression, self.prom_get)
        class Relay(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_GET(self):
                try:
                    status, raw, media = chain.gate.read(self.path, self.headers.get('Authorization'))
                    self.send_response(status); self.send_header('Content-Type',media)
                    self.send_header('Content-Length',str(len(raw))); self.end_headers(); self.wfile.write(raw)
                except BaseException as error:
                    # Never fabricate a Prom JSON response. Unexpected paths or
                    # upstream errors close this response and fail the package.
                    private_json(chain.work/'relay-error.json', {'error_type':type(error).__name__})
                    self.close_connection = True
        with ro.acquisition_signals():
            self.servers.append(OwnedServer(RELAY_PORT, Relay))
        config = {'global':{'scrape_interval':'1s'},'scrape_configs':[{'job_name':'uuid-capacity','static_configs':[
            {'targets':['host.docker.internal:'+str(EXPORT_PORT)]}]}]}
        ro.private_file(self.work/'prometheus.json', json.dumps(config)); self.config_hashes['prometheus.json'] = hashlib.sha256((self.work/'prometheus.json').read_bytes()).hexdigest()
        check(inspect_container(self.prom_name, self.work, 'before-create') is None, 'planned UUID Prom already exists; no adoption')
        self.prom_attempted = True
        cmd = ['docker','create','--pull','never','--name',self.prom_name]
        for key,value in self.labels.items(): cmd += ['--label', key+'='+value]
        cmd += ['--cpus','.5','--memory','256m','--memory-swap','256m','--read-only','--cap-drop','ALL',
                '--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),
                '--tmpfs','/prometheus:'+self.tmpfs,'-p','127.0.0.1:'+str(PROM_PORT)+':9090',
                '--mount','type=bind,source='+str(self.work/'prometheus.json')+',target=/etc/prometheus/owned.json,readonly',PROM_IMAGE,*self.prom_args]
        with ro.acquisition_signals():
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
            valid_create = result.returncode == 0 and bool(re.fullmatch('[a-f0-9]{64}',result.stdout.strip()))
            if valid_create: self.prom_cid = result.stdout.strip()  # Retain before fallible receipt I/O.
            private_json(self.work/'prom-create.json', {'returncode':result.returncode,'stdout':result.stdout[:32768],'stderr':result.stderr[:32768]})
            check(valid_create, 'Prom create outcome unknown; no CID adoption/retry')
        guard_prom(inspect_container(self.prom_cid, self.work, 'created'), self, False)
        check(subprocess.run(['docker','start',self.prom_cid], capture_output=True, text=True, timeout=10).returncode == 0, 'own Prom failed to start')
        guard_prom(inspect_container(self.prom_cid, self.work, 'started'), self)
        deadline = time.monotonic()+25
        while time.monotonic() < deadline:
            try:
                status, raw, _ = self.prom_get('/api/v1/query?'+urlencode({'query':self.expression}))
                value = json.loads(raw); found = value.get('data',{}).get('result',[])
                if status == 200 and len(found) == 1 and found[0]['metric'].get('signal_id') == self.nonce and float(found[0]['value'][1]) == 7:
                    break
            except (OSError, http.client.HTTPException): pass
            time.sleep(.2)
        else: raise Blocked('own real Prom scrape not ready; no synthetic matrix fallback')
        # Query_range ends are truncated and step=30; let one real sample lie
        # before the next endpoint. This is readiness, not sourcehistory proof.
        time.sleep(2)
        self.marker_case('source', dict(prometheus_cid=self.prom_cid, image_id=PROM_IMAGE,
            synthetic_exposition=True, real_scrape=True, model_reads=0, shared_source_modified=False))

    def setup_http(self):
        self.call('admin','POST','/api/v1/services',{'items':[{'id':self.app_key,'tier':'T1'}]}, want=201, scenario='fixture',
                  validate=lambda v:check(v == {'created':1}, 'formal service producer differs'))
        self.app = self.sql('SELECT id FROM applications WHERE key='+q(self.app_key), read=True)
        check(self.app and '\n' not in self.app, 'exact HTTP application missing')
        # The fifth actor is the already HTTP-authenticated own bootstrap
        # administrator, with no pending read; this distinguishes a source cap
        # from a hypothetical per-user single-pending limit. Read identity only.
        self.admin_actor = self.sql('SELECT id FROM users WHERE email='+q(self.admin_email)+" AND role='admin'", read=True)
        check(self.admin_actor and '\n' not in self.admin_actor and self.admin_actor not in self.users.values(),
              'exact own authenticated fifth administrator identity missing')
        created, _ = self.call('admin','POST','/api/v1/connectors', dict(type='prometheus',auth='none',url=self.relay_url,webhook=False,tools=['query_metrics']),
                               want=201, scenario='fixture')
        self.connector = created['id']; encoded = base64.urlsafe_b64encode((self.app+'\x00test').encode()).decode().rstrip('=')
        target, _ = self.call('admin','POST','/api/v1/services/'+encoded+'/external-targets',
            dict(connector_id=self.connector,target_kind='metric_resource',external_id=self.resource_id), want=201, scenario='fixture')
        check(target.get('connector_id') == self.connector and target.get('environment_id') == 'test'
              and target.get('external_id') == self.resource_id and target.get('target_kind') == 'metric_resource', 'formal target binding differs')
        self.target = target['id']
        # All four people get genuine scoped permissions before the scenario.
        for user in self.users:
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x) for x in
                (self.roles[user], 'services', 'view', 'application', self.app))+') ON CONFLICT DO NOTHING;')
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x) for x in
                (self.roles[user], 'environment', 'view', 'environment', 'test'))+') ON CONFLICT DO NOTHING;')
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x) for x in
                (self.roles[user], 'connectors', 'view', 'environment', 'test'))+') ON CONFLICT DO NOTHING;')
        pull = dict(sources=[dict(id=self.source_id,name='UUID pending capacity',application_id=self.app,environment_id='test',
            connector_id=self.connector,target_id=self.target,resource_id=self.resource_id,query_id='capacity_signal',expression=self.expression,
            name_labels=['service'],label_keys=['signal_id'])])
        ro.private_file(self.work/'pull.json', json.dumps(pull)); self.config_hashes['pull.json'] = hashlib.sha256((self.work/'pull.json').read_bytes()).hexdigest()
        self.configured = True; self.stop_generation(); self.start(); self.pull_path = '/api/v1/ingest/pull/'+self.source_id

    def actor_id(self, user):
        actor = self.admin_actor if user == 'admin' else self.users[user]
        check(bool(actor), 'exact authenticated receipt actor missing')
        return actor

    def capture_io(self):
        # Also retain native first-red receipts when a timing/ledger assertion
        # stops before record(). Never retain session cookies or request headers.
        for key,response in self.responses.items():
            row = {k:response[k] for k in ('status','error_type','index') if k in response}
            row['actor_id'] = self.actor_id(response['user'])
            if 'raw' in response:
                row.update(body=response['raw'].decode('utf-8','replace'), sha256=hashlib.sha256(response['raw']).hexdigest())
            private_json(self.work/('received-'+key+'.json'), row)
        if self.gate:
            with self.gate.condition: receipts = dict(self.gate.receipts)
            for index,receipt in receipts.items():
                private_json(self.work/('original-source-'+str(index)+'.json'),dict(status=receipt['status'],
                    body=receipt['raw'].decode('utf-8','replace'),body_sha256=receipt['sha256'],path=receipt['path'],
                    gate_expired=self.gate.expired))

    def ledger(self, deadline=None):
        fields = []
        for key, table, clause in [('attempts','v3_pull_attempts',''), ('identities','v3_pull_source_identities',''),
                                   ('audit','management_audit'," WHERE action LIKE 'v3.ingest_pull.%'")]:
            fields.append(q(key)+",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+table+' t'+clause+')')
        stmt = 'BEGIN READ ONLY; SELECT json_build_object('+','.join(fields)+'); COMMIT;'
        return json.loads(self.pg(stmt, self.database, self.db_user, timeout=2, deadline=deadline))

    def request(self, key, user, index, timeout=7):
        try:
            status, raw, headers = ro.transport(ro.BASES[index], 'GET', self.pull_path, headers={
                'Cookie':self.cookies[user], 'Origin':ro.BASES[index], 'X-AIOps-Environment':'test', 'Accept-Language':'en'}, timeout=timeout)
            check(len(raw) <= 65536, 'formal response too large')
            self.responses[key] = dict(status=status, raw=raw, headers=headers, user=user, index=index)
        except BaseException as error: self.responses[key] = dict(error_type=type(error).__name__, user=user, index=index)

    def record(self, key, expected, receipt=None):
        response = self.responses[key]; self.calls += 1
        canonical, kind = self.contract.canonical('GET', self.pull_path)
        row = dict(name='pull_capacity.http.'+key, method='GET', path=canonical, route_class=kind, scenario='boundary',
            run_id=self.run_id, call_id=self.run_id+'-'+str(self.calls), test_name='FormalPullPendingCapacity', expected={'status':[expected]},
            actual={'status':response.get('status')}, evidence_mode='real_local')
        try:
            check(response.get('status') == expected, 'formal response status differs')
            value = json.loads(response['raw'])
            if expected == 429:
                check(value.get('error',{}).get('code') == 'rate_limited'
                      and {k.lower():v for k,v in response['headers'].items()}.get('retry-after') == '60', 'source-cap429 contract differs')
            else: response['rows'] = validate_dto(value, receipt, self)
            row['actual'].update(response_sha256=hashlib.sha256(response['raw']).hexdigest(), api_port=PORTS[response['index']],
                actor_id=self.actor_id(response['user']), rows=response.get('rows'))
            row['status'] = 'passed'
        except BaseException as error:
            row['status'] = 'failed'; row['actual']['error_type'] = type(error).__name__; self.first_red(row)
            ro.append_json(self.work/'interfaces.jsonl', row); emit(row['name'],'failed',row['actual'],interface=row); raise
        private_json(self.work/('http-'+key+'.json'), {'status':response['status'],'body':json.loads(response['raw']),
            'sha256':hashlib.sha256(response['raw']).hexdigest(),'actor_id':self.actor_id(response['user']),'port':PORTS[response['index']]})
        ro.append_json(self.work/'interfaces.jsonl', row); emit(row['name'],'passed',row['actual'],interface=row)

    def scenario(self):
        self.frozen(); baseline = self.ledger(); check(baseline == {'attempts':[],'identities':[],'audit':[]}, 'fresh Pull ledger not empty')
        plan = [('reader',0),('peer',1),('hidden',0),('denied',1)]
        for i,(user,index) in enumerate(plan):
            thread = threading.Thread(target=self.request,args=(str(i),user,index),daemon=False)
            self.workers.append(thread); thread.start()
        try:
            deadline = self.gate.wait_four()
            before = self.ledger(deadline=deadline-.5); pending_four(before, [self.users[u] for u,_ in plan], self.source_id)
            if time.monotonic() >= deadline-.5: raise Blocked('actual four-pending observation window too short; no retry')
            check(self.admin_actor and self.admin_actor not in [self.users[u] for u,_ in plan]
                  and not any(row['actor_id'] == self.admin_actor for row in before['attempts']),
                  'fifth authorized actor must have zero pending reads')
            self.request('fifth','admin',1,timeout=min(.7,deadline-time.monotonic()-.2))
            fifth = self.responses['fifth']
            check(fifth.get('status') == 429, 'fifth request did not reach source-cap rejection')
            after = self.ledger(deadline=deadline-.1)
            if self.gate.expired or time.monotonic() >= deadline:
                raise Blocked('four-pending rejection observation window expired; no retry')
            check(before == after and len(self.gate.arrivals) == 4 and len(self.gate.receipts) == 4,
                  'rejected call wrote ledger or reached source')
        finally: self.gate.release.set()
        for thread in self.workers: thread.join(8)
        check(all(not t.is_alive() for t in self.workers) and not self.gate.expired, 'held readers did not finish within original provider deadline')
        self.marker_case('pending_four', {'actual_pending':4,'pending_users':4,'authorized_users':5,'fifth_actor_id':self.admin_actor,'fifth_actor_pending':0,'API_instances':2})
        self.record('fifth',429); self.marker_case('fifth_rejected', {'status':429,'retry_after':60})
        self.marker_case('no_rejected_io', {'attempts_added':0,'audits_added':0,'source_requests_added':0})
        # Arrival order can differ from client launch order. Match actual source
        # receipts by the exact registered query window encoded in each DTO.
        counts = collections.defaultdict(list)
        remaining = list(self.gate.receipts.values())
        for i,(user,index) in enumerate(plan):
            value = json.loads(self.responses[str(i)].get('raw', b'{}'))
            # Match full projections, including actual timestamps. Equal query
            # windows may have equal receipts, each still consumed once.
            matched = None
            for candidate in remaining:
                try: validate_dto(value,candidate,self); matched=candidate; break
                except AssertionError: pass
            check(matched is not None, 'missing actual source receipt')
            remaining.remove(matched); self.record(str(i),200,matched); counts[self.users[user]].append(self.responses[str(i)]['rows'])
        four = self.ledger(); check_completed(four,[self.users[u] for u,_ in plan],self.source_id,self.app,counts)
        self.request('sixth','peer',0); check(len(self.gate.arrivals) == 5 and len(self.gate.receipts) == 5, 'capacity recovery source count differs')
        self.record('sixth',200,self.gate.receipts[4]); counts[self.users['peer']].append(self.responses['sixth']['rows'])
        final = self.ledger(); check_completed(final,[self.users[u] for u,_ in plan]+[self.users['peer']],self.source_id,self.app,counts)
        check(final['identities'] == four['identities'], 'capacity recovery changed source identity')
        for index,receipt in self.gate.receipts.items(): private_json(self.work/('prom-response-'+str(index)+'.json'),
            {'status':receipt['status'],'body':json.loads(receipt['raw']),'body_sha256':receipt['sha256'],'path':receipt['path']})
        private_json(self.work/'capacity-ledger.json',{'pending':before,'rejected':after,'four_completed':four,'final':final})
        self.marker_case('release_recovery', {'completed_four':4,'sixth_status':200,'no_pending':True,'source_reads':5})
        self.marker_case('audit_consistency', {'success_attempts':5,'success_audits':5,'source_identity_preserved':True,'normal_added':0})

    def run(self):
        self.provision(); self.start(); self.seed()
        for user in ('admin',*self.users): self.login(user)
        self.start_source(); self.setup_http(); self.scenario(); self.frozen()
        return dict(run_id=self.run_id, source_tree_sha=SOURCE, source_tree_sha_end=SOURCE, binary_sha256=BINARY,
            runtime_sha256=RUNTIME, required_cases=list(REQUIRED_CASES), ports=list(ALL_PORTS), pull_requests=6,
            normal_routes=[], normal_added=0, real_source_requests=5, synthetic_identity_and_exposition=True,
            unverified=['source60/min budget','crash or pending expiry recovery','inflight revoke (reuse existing Go layer)',
                        'real external Prometheus credentials','continuous history','performance','24h','full business chain'])

    def cleanup(self):
        self.cleanup_deadline = time.monotonic()+40; errors=[]
        if self.gate: self.gate.release.set()
        for worker in self.workers:
            worker.join(max(0,min(8,self.cleanup_deadline-time.monotonic())))
            if worker.is_alive(): errors.append(Blocked('own request worker still alive'))
        try: self.capture_io()
        except BaseException as error: errors.append(error)
        for server in reversed(self.servers):
            try: server.close(self.cleanup_deadline)
            except BaseException as error: errors.append(error)
        try: _readonly_cleanup(self)
        except BaseException as error: errors.append(error)
        if self.prom_attempted:
            try:
                target = self.prom_cid or self.prom_name
                info = inspect_container(target,self.work,'cleanup-before',timeout=max(.1,min(3,self.cleanup_deadline-time.monotonic())),receipt_errors=errors)
                if info is not None:
                    check(self.prom_cid is not None, 'create outcome unknown; CID never held; no deletion/adoption')
                    guard_prom(info,self,False)
                    left = self.cleanup_deadline-time.monotonic(); check(left > 0,'cleanup deadline exhausted')
                    result = subprocess.run(['docker','rm','-f',self.prom_cid],capture_output=True,text=True,timeout=min(5,left))
                    try:
                        private_json(self.work/'prom-remove.json',dict(returncode=result.returncode,stdout=result.stdout[:32768],stderr=result.stderr[:32768]))
                    except BaseException as error: errors.append(error)
                    check(inspect_container(self.prom_cid,self.work,'cleanup-after',timeout=max(.1,min(3,self.cleanup_deadline-time.monotonic())),receipt_errors=errors) is None, 'own Prom remains')
            except BaseException as error: errors.append(error)
        for port in ALL_PORTS:
            with socket.socket() as sock:
                if sock.connect_ex(('127.0.0.1',port)) == 0: errors.append(Blocked('owned listener remains; no signaling foreign resources'))
        if errors: raise errors[0]
        emit('pull_capacity.cleanup','passed',{'run_id':self.run_id,'single_deadline_seconds':40,'grace_seconds':45,'own_CID_only':True,'workers_joined':True,'ports_absent':True})

def describe():
    return dict(module=MODULE, source_tree_sha=SOURCE, binary_sha256=BINARY, runtime_sha256=RUNTIME,
        postgres_cid=PG_CID, prometheus_image_id=PROM_IMAGE, ports=list(ALL_PORTS), helper_sha256=hashes(),
        required_cases=list(REQUIRED_CASES), timeout_seconds=300, cleanup_grace_seconds=45,
        source_CPU=.5, source_memory_bytes=268435456, private_tmpfs_bytes=33554432,
        upstream_deadline_seconds=5, hold_window_seconds=HOLD_SECONDS, pull_requests=6, normal_added=0)

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__); parser.add_argument('--describe',action='store_true')
    parser.add_argument('--live',action='store_true'); parser.add_argument('--run-id'); parser.add_argument('--admission',type=Path)
    args=parser.parse_args(argv)
    if args.describe: print(json.dumps(describe())); return 0
    if not args.live or not args.run_id or not args.admission: parser.error('fresh root --live/--run-id/--admission required')
    check(str(uuid.UUID(args.run_id)) == args.run_id,'canonical resource UUID required')
    fd=os.open(ROOT/'.local/testlab/pull-capacity.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB); admission(args.admission,args.run_id)
        with ro.cooperative_signals(): ro.execute(Chain(doctor(),args.run_id))
        return 0
    except Blocked as error: emit('pull_capacity.environment','blocked',{'reason':str(error)}); return 2
    except BaseException as error: emit('pull_capacity.failure','failed',{'error_type':type(error).__name__}); return 1
    finally: os.close(fd)

if __name__=='__main__': raise SystemExit(main())

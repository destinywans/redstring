"""Formal Pull and built-in synthetic Push acceptance with owned real scraping.

Only HTTP creates business state. SQL writes synthetic identities/grants only.
One /signal changes the owned shared exporter and may append local Alertmanager
captures; runtime notification destinations must be proved local before it runs.
There is no production notification, history, vendor ingress or SLA claim.
"""
from __future__ import annotations
import argparse,base64,contextlib,datetime,hashlib,hmac,http.client,json,math,os,re,secrets,select,socket,stat,subprocess,threading,time,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs,urlencode,urlsplit
import api_readonly_chain as readonly

ROOT,OWNER=readonly.ROOT,readonly.OWNER
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SOURCE_SHA='6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
AM_SOURCE_SHA='03e08321aaa98b1a7bdd3ad5150a8211f5ba128e1e27d3a5842b6784b07d2699'
MODULE='formal-api-ingress';RELAY_PORT=18616;RELAY_URL='http://127.0.0.1:'+str(RELAY_PORT)
TYPES=('alert','change','incident','resource','oncall')
TARGETS=(('GET','/ingest/pull/{id}'),('POST','/ingest/pull/{id}/test'),('POST','/ingest/push/test'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('GET','/service-mappings'),('POST','/connectors'),
         ('POST','/services/{id}/external-targets'),('GET','/ingest/push'))
REQUIRED_CASES=('ingress.source_scope','ingress.real_signal','ingress.private_source_configuration',
 'ingress.pull.preview','ingress.pull.test','ingress.push.five_types','ingress.authorization_boundary',
 'ingress.pull.failure_recovery','ingress.pull.budget','ingress.revocation','ingress.audit_consistency')
COMPONENTS={'prometheus':(18570,'9090','prom/prometheus:v3.13.3',('prometheus.yml','testlab-rules.yml')),
 'alertmanager':(18571,'9093','prom/alertmanager:v0.34.1',('alertmanager.yml',))}
check,Blocked=readonly.check,readonly.Blocked


def emit(name,status,actual,**fields):
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)


def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def helper_hashes():
    return {name:digest(ROOT/name) for name in ('tools/testlab/api_readonly_chain.py','tools/testlab/api_ingress_coverage.py',
      'tools/testlab/fixtures.py','tools/testlab/components/prometheus.yml','tools/testlab/components/testlab-rules.yml','tools/testlab/components/alertmanager.yml')}


def component_guard(provider,info,pinned=None):
    port,internal,image,files=COMPONENTS[provider];name='aiops-'+OWNER+'-'+provider
    config=info.get('Config',{});folder=ROOT/'tools/testlab/components'
    destination='/etc/prometheus/' if provider=='prometheus' else '/etc/alertmanager/'
    if (info.get('Name')!='/'+name or config.get('Image')!=image or config.get('Labels',{}).get('aiops.testlab.owner')!=OWNER or
      not info.get('State',{}).get('Running') or not re.fullmatch(r'[a-f0-9]{64}',str(info.get('Id',''))) or
      not re.fullmatch(r'sha256:[a-f0-9]{64}',str(info.get('Image',''))) or
      info.get('NetworkSettings',{}).get('Ports',{}).get(internal+'/tcp')!=[dict(HostIp='127.0.0.1',HostPort=str(port))]):
        raise Blocked('owned '+provider+' CID/image/name/port mismatch')
    for file in files:
        matches=[m for m in info.get('Mounts',[]) if m.get('Source')==str(folder/file) and m.get('Destination')==destination+file and m.get('RW') is False]
        if len(matches)!=1:raise Blocked('owned component config/rule readonly mount mismatch')
    identity=dict(name=name,container_id=info['Id'],image_id=info['Image'],port=port,image=image)
    if pinned is not None and identity!=pinned:raise Blocked('owned component immutable identity changed')
    return identity


def wire(port,method,path,body=None):
    parsed=urlsplit(path)
    endpoints={18578:{('GET','/health'),('POST','/signal')},18570:{('GET','/api/v1/status/config'),('GET','/api/v1/query'),('GET','/api/v1/query_range')},18571:{('GET','/api/v2/status'),('GET','/metrics')}}
    if (method,parsed.path) not in endpoints.get(port,set()) or parsed.scheme or parsed.netloc or parsed.fragment or '\\' in path or len(path)>32768 or any(ord(c)<32 for c in path):
        raise Blocked('source HTTP operation outside owned fixed port/endpoint boundary')
    connection=http.client.HTTPConnection('127.0.0.1',port,timeout=5)
    try:
        payload=json.dumps(body).encode() if body is not None else None
        connection.request(method,path,payload,{'Content-Type':'application/json'} if payload is not None else {})
        response=connection.getresponse();raw=response.read((2<<20)+1)
        if len(raw)>2<<20:raise Blocked('owned ingress source exceeded evidence budget')
        return response.status,raw
    finally:connection.close()


def fixture_challenge(root,pinned=None):
    record=readonly.private_json(Path(root)/'.local/testlab/component-environment.json')
    work=Path(root)/'.local/testlab/environments'/OWNER;path=work/'fixture-owner-token'
    if record.get('owner')!=OWNER or Path(record.get('work','')).resolve()!=work.resolve() or record.get('fixture_sha')!=digest(ROOT/'tools/testlab/fixtures.py'):
        raise Blocked('owned fixture registry/source mismatch')
    if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600:raise Blocked('private fixture challenge secret required')
    nonce=secrets.token_hex(24);status,raw=wire(18578,'GET','/health?challenge='+nonce);value=json.loads(raw)
    pid=record.get('fixture_pid');token=path.read_text().strip()
    proof=hmac.new(token.encode(),f'{nonce}|{pid}|{OWNER}'.encode(),hashlib.sha256).hexdigest()
    if status!=200 or type(pid) is not int or value.get('pid')!=pid or value.get('owner')!=OWNER or not hmac.compare_digest(str(value.get('proof','')),proof) or not readonly._process_port(pid,18578):
        raise Blocked('fresh fixture nonce/HMAC/listener attestation failed')
    identity=dict(pid=pid,owner=OWNER,token_sha256=digest(path),fixture_sha=record['fixture_sha'],port=18578)
    if pinned is not None and identity!=pinned:raise Blocked('fixture immutable invocation identity changed')
    return identity


def _yaml_scalar(value):
    value=value.strip()
    if value.startswith('"'):
        try:return json.loads(value)
        except ValueError:raise Blocked('unsupported runtime receiver scalar') from None
    if value.startswith("'") and value.endswith("'"):return value[1:-1].replace("''", "'")
    if not value or re.fullmatch(r'[A-Za-z0-9_.:/-]+',value):return value
    raise Blocked('unsupported runtime receiver shape; cannot prove local destinations')


def validate_local_runtime(prometheus,alertmanager,masked=False):
    """Fail-closed lexical subset of already validated runtime configurations.

    No general YAML deserialization or permissive substring destination check.
    Unknown receiver fields, aliases, URLs, proxies or channels block ingestion.
    """
    if not isinstance(prometheus,str) or not isinstance(alertmanager,str):raise Blocked('runtime config text absent')
    top=re.findall(r'(?m)^([a-zA-Z_][a-zA-Z0-9_]*):([^\n]*)$',alertmanager)
    allowed_top={'route','receivers','templates'}|({'global'} if masked else set())
    names=[key for key,_ in top]
    if len(names)!=len(set(names)) or any(key not in allowed_top for key in names):
        raise Blocked('unknown/duplicate AM top-level block may add an outbound channel')
    if any(key=='templates' and value.strip()!='[]' for key,value in top):
        raise Blocked('only empty AM template files are proved in this owned configuration')
    # Pinned AM adds passive provider URL defaults in status.String(). They do
    # not enable receivers. Permit only these exact defaults; additional HTTP
    # transports, credentials, event_recorders or tracing remain unproved.
    defaults={'resolve_timeout':'5m','http_config':'','follow_redirects':'true','enable_http2':'true',
      'smtp_hello':'localhost','smtp_require_tls':'true','smtp_tls_config':'','insecure_skip_verify':'false',
      'slack_app_url':'https://slack.com/api/chat.postMessage','pagerduty_url':'https://events.pagerduty.com/v2/enqueue',
      'opsgenie_api_url':'https://api.opsgenie.com/','wechat_api_url':'https://qyapi.weixin.qq.com/cgi-bin/',
      'victorops_api_url':'https://alert.victorops.com/integrations/generic/20131114/alert/',
      'telegram_api_url':'https://api.telegram.org','webex_api_url':'https://webexapis.com/v1/messages','rocketchat_api_url':'https://open.rocket.chat/'}
    global_block=re.search(r'(?m)^global:\s*\n([\s\S]*?)(?=^[A-Za-z_]|\Z)',alertmanager)
    if global_block:
        observed=set()
        for line in global_block[1].splitlines():
            if not line.strip():continue
            entry=re.fullmatch(r'\s+([a-z0-9_]+):\s*(.*?)\s*',line)
            if not entry or entry[1] in observed or entry[1] not in defaults or _yaml_scalar(entry[2])!=defaults[entry[1]]:
                raise Blocked('unknown or changed AM global transport/default')
            observed.add(entry[1])
    prom_keys={'global','scrape_interval','scrape_timeout','evaluation_interval','metric_name_validation_scheme','scrape_native_histograms','extra_scrape_metrics','runtime','gogc','alerting','alertmanagers','follow_redirects','enable_http2','scheme','timeout','api_version','static_configs','targets','rule_files','scrape_configs','job_name','honor_timestamps','track_timestamps_staleness','scrape_protocols','always_scrape_classic_histograms','convert_classic_histograms_to_nhcb','metrics_path','enable_compression','metric_name_escaping_scheme','storage','tsdb','outofordertimewindow','retention','time','otlp','translation_strategy','label_name_underscore_sanitization','label_name_preserve_multiple_underscores'}
    prom_lists={'alertmanager:9093','host.docker.internal:18578','/etc/prometheus/testlab-rules.yml','OpenMetricsText1.0.0','OpenMetricsText0.0.1','PrometheusText1.0.0','PrometheusText0.0.4'}
    for line in prometheus.splitlines():
        if not line.strip():continue
        item=re.fullmatch(r'\s*-\s+(.+?)\s*',line)
        if item and item[1] in prom_lists:continue
        field=re.fullmatch(r'\s*(?:-\s*)?([a-z0-9_]+):\s*(.*?)\s*',line)
        if field:
            if field[1] not in prom_keys:raise Blocked('unknown Prom runtime field may add a nonlocal channel')
            scalar=_yaml_scalar(field[2])
            if field[1]=='scheme' and scalar!='http' or field[1]=='metrics_path' and scalar!='/metrics':raise Blocked('unexpected Prom source transport')
        else:
            item=re.fullmatch(r'\s*-\s+(.+?)\s*',line)
            if not item or _yaml_scalar(item[1]) not in prom_lists:raise Blocked('unknown Prom runtime list/source path')
    if [_yaml_scalar(x) for x in re.findall(r'(?m)^\s*receiver:\s*(.+)$',alertmanager)]!=['testlab-capture']:
        raise Blocked('single local notification route must be explicit')
    jobs=[_yaml_scalar(x) for x in re.findall(r'(?m)^\s*-?\s*job_name:\s*(.+)$',prometheus)]
    endpoints=[_yaml_scalar(x) for x in re.findall(r'(?m)^\s*-\s*([^\s]+:\d+)\s*$',prometheus)]
    if jobs!=['testlab-business'] or sorted(endpoints)!=['alertmanager:9093','host.docker.internal:18578'] or re.search(r'\w+_sd_configs:',prometheus):
        raise Blocked('owned Prom runtime scrape/alert destinations are not exact local sources')
    if re.search(r'(?m)^\s*proxy_from_environment:\s*true\s*$',prometheus) or re.search(r'(?m)^\s*proxy_url:\s*(?!""\s*$|\s*$)\S',prometheus):
        raise Blocked('owned Prom runtime proxy outside local isolation')
    section=re.search(r'(?m)^receivers:\s*$',alertmanager)
    if section is None:raise Blocked('runtime receiver section missing')
    lines=alertmanager[section.end():].splitlines();fields=[]
    allowed={'name','labels','webhook_configs','send_resolved','http_config','follow_redirects','enable_http2','proxy_url','no_proxy','proxy_from_environment','url','url_file','max_alerts','timeout'}
    for line in lines:
        if not line.strip():continue
        if re.match(r'^[A-Za-z_][A-Za-z0-9_]*:',line):break
        match=re.fullmatch(r'\s*(?:-\s*)?([a-z0-9_]+):\s*(.*?)\s*',line)
        if not match or match[1] not in allowed:raise Blocked('unproved receiver field/channel in runtime config')
        key=match[1]
        value='<secret>' if masked and key=='url' and match[2]=='<secret>' else _yaml_scalar(match[2])
        fields.append(('receiver_name' if key=='name' and line.lstrip().startswith('-') else key,value))
        if key in ('proxy_url','no_proxy','url_file') and value!='':raise Blocked('receiver proxy/file destination is not explicitly local')
        if key=='proxy_from_environment' and value!='false':raise Blocked('receiver environment proxy could escape isolation')
        if key in ('webhook_configs','http_config','labels') and value!='':raise Blocked('unsupported receiver nested shape')
        if key=='name' and value!='testlab-capture':raise Blocked('receiver label/name outside exact capture scope')
    values=lambda key:[v for k,v in fields if k==key]
    expected_url='<secret>' if masked else 'http://host.docker.internal:18578/capture/alertmanager'
    if values('receiver_name')!=['testlab-capture'] or values('webhook_configs')!=[''] or values('url')!=[expected_url]:
        raise Blocked('all runtime receivers must target only owned local Alertmanager capture')
    return dict(real_external_notifications=False if not masked else None,local_capture_possible=True,destination_verified_by_status=not masked,runtime_config_sha256=dict(prometheus=hashlib.sha256(prometheus.encode()).hexdigest(),alertmanager=hashlib.sha256(alertmanager.encode()).hexdigest()))


def source_fd_snapshot(path):
    path=Path(path)
    if path.resolve()!=path or path.is_symlink():raise Blocked('receiver source path must contain no symlink')
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW)
    try:
        before=os.fstat(fd)
        if not stat.S_ISREG(before.st_mode) or before.st_size>65536:raise Blocked('receiver source must be bounded regular file')
        raw=os.read(fd,65537);after=os.fstat(fd);current=path.stat(follow_symlinks=False)
        metadata=lambda s:(s.st_dev,s.st_ino,s.st_size,s.st_ctime_ns,s.st_mtime_ns)
        if metadata(before)!=metadata(after) or metadata(after)!=metadata(current) or len(raw)!=before.st_size:
            raise Blocked('receiver source FD/path identity drift while reading')
        return dict(path=str(path),device=before.st_dev,inode=before.st_ino,size=before.st_size,ctime_ns=before.st_ctime_ns,mtime_ns=before.st_mtime_ns,sha256=hashlib.sha256(raw).hexdigest(),body=raw.decode('utf-8'))
    finally:os.close(fd)


def container_receiver_bytes(cid):
    """Read only the fixed mounted path in an already attested exact container.

    A single-file bind can retain an old inode after a host atomic rename. Host
    path metadata therefore cannot substitute for these actual container bytes.
    """
    if not re.fullmatch(r'[a-f0-9]{64}',str(cid)):raise Blocked('exact receiver container identity required')
    proc=subprocess.Popen(['docker','exec',cid,'/bin/cat','/etc/alertmanager/alertmanager.yml'],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
    deadline=time.monotonic()+5;chunks=[];size=0
    try:
        while True:
            remaining=deadline-time.monotonic()
            if remaining<=0:raise Blocked('bounded receiver container read timed out')
            if not select.select([proc.stdout],[],[],remaining)[0]:raise Blocked('bounded receiver container read timed out')
            chunk=os.read(proc.stdout.fileno(),min(4096,65537-size))
            if not chunk:break
            chunks.append(chunk);size+=len(chunk)
            if size>65536:raise Blocked('receiver container config exceeded evidence budget')
        if proc.wait(timeout=max(.001,deadline-time.monotonic()))!=0:raise Blocked('fixed receiver container config read failed')
        return b''.join(chunks)
    except subprocess.TimeoutExpired:raise Blocked('receiver container read process timed out') from None
    finally:
        proc.stdout.close()
        if proc.poll() is None:
            proc.terminate()
            try:proc.wait(timeout=1)
            except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=1)


def validate_receiver_load(info,source,container_body,metrics,prometheus,masked_status,source_sha,expected=None,now=None):
    identity=component_guard('alertmanager',info)
    if info.get('Path')!='/bin/alertmanager' or info.get('Args')!=['--config.file=/etc/alertmanager/alertmanager.yml','--storage.path=/alertmanager']:
        raise Blocked('receiver argv must resolve one exact readonly mounted configuration')
    values={}
    for name in ('alertmanager_config_last_reload_successful','alertmanager_config_last_reload_success_timestamp_seconds','alertmanager_config_hash'):
        matched=re.findall(r'(?m)^'+name+r'\s+([^\s]+)\s*$',metrics)
        if len(matched)!=1:raise Blocked('receiver successful-load metric absent or ambiguous')
        try:values[name]=float(matched[0])
        except ValueError:raise Blocked('invalid receiver load metric') from None
        if not math.isfinite(values[name]):raise Blocked('receiver load metric is not finite')
    loaded=values['alertmanager_config_last_reload_success_timestamp_seconds']
    try:started=datetime.datetime.fromisoformat(info['State']['StartedAt'].replace('Z','+00:00')).timestamp()
    except (ValueError,KeyError):raise Blocked('receiver container StartedAt proof unavailable') from None
    if values['alertmanager_config_last_reload_successful']!=1 or loaded<started or loaded>(time.time() if now is None else now):
        raise Blocked('receiver load must be successful, after container start and not in the future')
    expected_path=str(ROOT/'tools/testlab/components/alertmanager.yml')
    if source.get('path')!=expected_path or source.get('sha256')!=source_sha or hashlib.sha256(source['body'].encode()).hexdigest()!=source_sha or max(source['ctime_ns'],source['mtime_ns'])>int(loaded*1e9):
        raise Blocked('fixed receiver source changed after successful load or hash/path differs')
    if not isinstance(container_body,bytes) or container_body!=source['body'].encode() or hashlib.sha256(container_body).hexdigest()!=source_sha:
        raise Blocked('actual receiver mounted bytes differ from fixed host source')
    # Official pinned config coordinator hashes original LoadFile bytes; 48 bits
    # fit float64 exactly. Auxiliary reload witness, not secret authentication.
    config_hash=float(int.from_bytes(hashlib.md5(container_body).digest()[:6],'little'))
    if values['alertmanager_config_hash']!=config_hash:raise Blocked('actual loaded receiver config hash differs from mounted bytes')
    unmasked=validate_local_runtime(prometheus,source['body'])
    masked=validate_local_runtime(prometheus,masked_status,masked=True)
    bound=dict(component=identity,started_at=info['State']['StartedAt'],path=info['Path'],argv=info['Args'],source_fd={k:v for k,v in source.items() if k!='body'},container_config_sha256=hashlib.sha256(container_body).hexdigest(),config_hash_metric=config_hash,successful_load_seconds=loaded,runtime_config_sha256=masked['runtime_config_sha256'])
    if expected is not None and bound!=expected:raise Blocked('receiver CID/StartedAt/source FD/load/runtime drift across acceptance')
    return dict(binding=bound,local_delivery=unmasked,destination_basis='exact config argv/RO mount + actual container bytes == source FD + ctime/mtime before successful load + official config hash; masked status supplies receiver structure only')


def doctor(root):
    data=readonly.doctor(root)
    if data['source_tree_sha']!=SOURCE_SHA:raise Blocked('formal ingress frozen Go source fingerprint differs')
    if digest(ROOT/'tools/testlab/components/alertmanager.yml')!=AM_SOURCE_SHA:raise Blocked('owned AM source differs from the reviewed sole-local configuration')
    if readonly.inspect_pg(data['postgres_container']).get('Id')!=PG_CID:raise Blocked('owned immutable PG CID changed')
    components={provider:component_guard(provider,readonly.inspect_pg('aiops-'+OWNER+'-'+provider)) for provider in COMPONENTS}
    fixture=fixture_challenge(root)
    with socket.socket() as sock:
        if sock.connect_ex(('127.0.0.1',RELAY_PORT))==0:raise Blocked('exclusive ingress source relay listener occupied')
    return dict(data,components=components,fixture=fixture,helper_sha256=helper_hashes())


class Contract(readonly.Contract):
    def canonical(self,method,path):
        raw=urlsplit(path)
        if raw.scheme or raw.netloc or raw.fragment or not raw.path.startswith('/api/v1/'):raise Blocked('formal ingress namespace required')
        short=raw.path[len('/api/v1'):]
        candidates=[p for verb,p in (*TARGETS,*SUPPORT) if verb==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(p)),short)]
        selected=short if short in candidates else candidates[0] if len(candidates)==1 else None
        if selected is None or method.lower() not in self.api['paths'].get(selected,{}):raise Blocked('operation outside bounded ingress acceptance')
        return '/api/v1'+selected,'main' if (method,selected) in self.main else 'auxiliary_or_historical'


class Relay:
    def __init__(self,chain):
        self.chain=chain;self.failed=False;self.lock=threading.Lock();self.receipts=[];self.closed=False
        relay=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    parsed=urlsplit(self.path);query=parse_qs(parsed.query)
                    if parsed.scheme or parsed.netloc or parsed.fragment or parsed.path!='/api/v1/query_range' or query.get('query')!=[relay.chain.expression] or self.headers.get('Authorization'):
                        raise Blocked('relay accepts only registered exact Prom range expression')
                    relay.chain.frozen()
                    with relay.lock:failed=relay.failed
                    status,raw=(503,b'{"error":"Explicit owned relay fault"}') if failed else wire(18570,'GET',self.path)
                    with relay.lock:relay.receipts.append(dict(status=status,body=raw.decode('utf-8','replace'),injected=failed))
                    readonly._private(relay.chain.evidence_work/'prom-range-receipts.json',relay.receipts)
                except (Blocked,OSError,http.client.HTTPException):status,raw=502,b'{"error":"Owned exact source unavailable"}'
                self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.send_header('Connection','close');self.end_headers();self.wfile.write(raw)
            def do_POST(self):self.send_error(405)
        self.server=ThreadingHTTPServer(('127.0.0.1',RELAY_PORT),Handler);self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def close(self):
        if self.closed:return
        self.server.shutdown();self.server.server_close();self.thread.join(4)
        check(not self.thread.is_alive() and self.server.socket.fileno()==-1,'owned ingress relay failed to close');self.closed=True


def validate_preview(value,chain):
    check(value.get('id')==chain.source_id and value.get('contract')=='metric' and value.get('contract_version')=='MetricSeries v1' and value.get('step')==3,'Pull source/contract identity differs')
    check(value['request']['host']==RELAY_URL and value['request']['path']=='/api/v1/query_range' and value['sample']['status']==200,'Pull request/sample provenance differs')
    rows=value['preview']['rows'];check(bool(rows) and len(rows)<=20,'Pull preview is empty/unbounded')
    for row in rows:
        check(row['service']==chain.app and row['name']=='testlab-api' and row['labels']=={'signal_id':chain.signal_id,'status':row['labels'].get('status')} and row['labels']['status'] in ('200','500'),'Pull row escaped source/label allowlist')
        expected=chain.signal['signals']+(100 if row['labels']['status']=='200' else 0)
        check(math.isfinite(float(row['value'])) and float(row['value'])==expected,'Pull value differs from exact controlled metric')
    check(json.loads(value['sample']['body'])==rows[:5] and chain.expression not in json.dumps(value),'Pull sample or expression redaction differs')
    counts={item['label'] for item in value['preview']['checks']}
    check('0 dropped' in counts and 'Complete bounded response' in counts,'Pull dropped/complete status differs')


def validate_test(value):
    check(value.get('ok') is True and type(value.get('rows')) is int and value['rows']>0 and value.get('dropped')==0 and isinstance(value.get('captured_at'),str),'Pull test lacks successful nonempty projection')
    captured=datetime.datetime.fromisoformat(value['captured_at'].replace('Z','+00:00'))
    check(captured.tzinfo is not None and abs((datetime.datetime.now(datetime.timezone.utc)-captured).total_seconds())<=60,'Pull captured_at is not a fresh real source read')


def validate_push_counts(value):
    check({row['id'] for row in value['types']}==set(TYPES) and all(row['today']=='0' and row['rejected']=='0%' for row in value['types']),'synthetic test was counted as real Push activity')


class Chain(readonly.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id);self.contract=Contract(root);self.evidence_work=self.work
        self.generation=0;self.config_hashes={};self.relay=None;self.receipts={};self.pull_expected=[];self.expression='';self.receiver_load=None
        self.source_id='ingress-'+self.nonce;self.resource_id='ingress-metric-'+self.nonce
        self.state.update(suite=MODULE,ports=[*readonly.PORTS,RELAY_PORT],source_components=data['components'],fixture_identity=data['fixture'],
          source_scope='controlled current testlab exporter metric; real Prometheus scrape; synthetic built-in Push only',
          actual_results_source='HTTP services/connectors/targets and ingress attempt/event/receipt/audit producers',
          local_capture_side_effect='one owned shared /signal changes metric label/counter; local Alertmanager capture may append; no capture deletion')
        readonly._private(self.work/'state.json',self.state)
    def frozen(self):
        if helper_hashes()!=self.data['helper_sha256']:raise Blocked('ingress helper/mounted component config changed')
        for provider,identity in self.data['components'].items():component_guard(provider,readonly.inspect_pg(identity['name']),identity)
        fixture_challenge(self.root,self.data['fixture'])
        for name,expected in self.config_hashes.items():
            path=self.evidence_work/name
            if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600 or digest(path)!=expected:raise Blocked('ingress private source configuration changed')
        super().frozen()
    def pg(self,*args,**kwargs):
        deadline=kwargs.get('deadline');timeout=3 if deadline is None else min(3,deadline-time.monotonic())
        if timeout<=0:raise Blocked('ingress SQL cleanup budget exhausted')
        if readonly.inspect_pg(self.data['postgres_container'],timeout=timeout).get('Id')!=PG_CID:raise Blocked('owned immutable PG CID changed')
        return super().pg(*args,**kwargs)
    def call(self,user,method,path,*args,**kwargs):
        self.contract.canonical(method,path)
        if kwargs.get('scenario','normal')=='normal' and kwargs.get('want',200) in range(200,300) and kwargs.get('validate') is None:raise Blocked('normal ingress route needs explicit business validator')
        self.frozen();return super().call(user,method,path,*args,**kwargs)
    def case_marker(self,name,value):
        check(name in REQUIRED_CASES,'unrecognized ingress stable marker')
        scope=dict(run_id=self.run_id,source_tree_sha=self.data['source_tree_sha'])
        check(all(value.get(key,expected)==expected for key,expected in scope.items()),'ingress marker run/source scope differs from its chain')
        actual=dict(value);actual.update(scope)
        self.frozen();emit(name,'passed',actual)
    def startup_environment(self):
        return {} if not self.config_hashes else {'AIOPS_V3_PULL_SOURCES_CONFIG':str(self.evidence_work/'pull.json'),'AIOPS_V3_PUSH_SOURCES_CONFIG':str(self.evidence_work/'push.json')}
    def start(self):
        generation=self.generation;self.generation+=1;folder=self.evidence_work/('startup-'+str(generation));folder.mkdir(mode=0o700,exist_ok=False)
        for name in ('instance.json','models.json','commands.json'):readonly.private_file(folder/name,(self.evidence_work/name).read_text())
        original_work,original_emit=self.work,readonly.emit
        def scoped(name,status,actual,**fields):
            original_emit(name+'.generation-'+str(generation) if name.startswith('readonly.api_ready.') else name,status,actual,**fields)
        self.work=folder;readonly.emit=scoped
        try:super().start()
        finally:
            self.work=original_work;readonly.emit=original_emit;readonly._private(self.evidence_work/'state.json',self.state)
    def stop_owned_generation(self):
        current=self.processes[-2:];check(len(current)==2,'two held subprocess handles required before source reconfiguration')
        for proc,port in zip(current,readonly.PORTS):
            if proc.poll() is None and not readonly._process_port(proc.pid,port):raise Blocked('held API child no longer owns its exact listener')
        for proc in reversed(current):
            if proc.poll() is None:
                proc.terminate()
                try:proc.wait(timeout=8)
                except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=2)
            check(proc.poll() is not None,'own API generation still running')
        self.state.setdefault('stopped_generations',[]).append(dict(generation=self.generation-1,pids=[proc.pid for proc in current]))
        readonly._private(self.evidence_work/'state.json',self.state)
    def seed(self):
        super().seed();self.sql('BEGIN; '+''.join('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(readonly.quote(v) for v in (self.roles['peer'],module,action,'environment','test'))+');' for module,action in (('services','change'),('connectors','manage')))+' COMMIT;')
    def source_scope(self):
        self.call('admin','POST','/api/v1/services',{'items':[dict(id='testlab',tier='T1')]},want=201,scenario='fixture',validate=lambda v:check(v=={'created':1},'HTTP service producer differs'))
        mapping,_=self.call('admin','GET','/api/v1/service-mappings',scenario='fixture');rows=[r for r in mapping['rows'] if r['service']=='testlab']
        check(len(rows)==1,'HTTP source service identity missing');self.app=rows[0]['save']['service_id']
        created,_=self.call('admin','POST','/api/v1/connectors',dict(type='prometheus',auth='none',url=RELAY_URL,webhook=False,tools=['query_metrics']),want=201,scenario='fixture',validate=lambda v:check(isinstance(v.get('id'),str) and bool(v['id']),'HTTP connector identity absent'))
        self.connector=created['id'];encoded=base64.urlsafe_b64encode((self.app+'\x00test').encode()).decode().rstrip('=')
        target,_=self.call('admin','POST','/api/v1/services/'+encoded+'/external-targets',dict(connector_id=self.connector,target_kind='metric_resource',external_id=self.resource_id),want=201,scenario='fixture',validate=lambda v:check(v.get('connector_id')==self.connector and v.get('provider')=='prometheus' and v.get('environment_id')=='test' and v.get('target_kind')=='metric_resource' and v.get('external_id')==self.resource_id and bool(v.get('id')),'HTTP metric target identity differs'))
        self.target=target['id'];self.case_marker('ingress.source_scope',dict(application=self.app,connector=self.connector,target=self.target,sql_business_result_writes=0))
    def real_signal(self):
        self.receiver_proof('before-signal')
        status,raw=wire(18578,'POST','/signal',{});readonly._private(self.work/'signal-receipt.json',dict(run_id=self.run_id,status=status,body=raw.decode(),writes=1,side_effect='owned shared exporter signal changes; local captures may append'))
        check(status==200,'single signal write failed; no automatic retry');self.signal=json.loads(raw);self.signal_id=self.signal.get('signal_id')
        check(re.fullmatch(r'[a-f0-9]{24}',str(self.signal_id)) and type(self.signal.get('signals')) is int and self.signal['signals']>0,'actual exporter signal identity absent')
        self.expression='testlab_requests_total{application="testlab",environment="test",signal_id="'+self.signal_id+'"}'
        deadline=time.monotonic()+30;attempts=[]
        while True:
            self.frozen();status,raw=wire(18570,'GET','/api/v1/query?'+urlencode({'query':self.expression}));value=json.loads(raw)
            result=value.get('data',{}).get('result',[]);attempts.append(dict(status=status,series=len(result)))
            readonly._private(self.work/'scrape-readiness.json',dict(attempts=attempts,last_response=value,run_id=self.run_id,signal_id=self.signal_id))
            if status==200 and len(result)==2 and {x['metric'].get('status') for x in result}=={'200','500'}:
                check(all(x['metric'].get('signal_id')==self.signal_id and x['metric'].get('application')=='testlab' and x['metric'].get('environment')=='test' and float(x['value'][1])==self.signal['signals']+(100 if x['metric']['status']=='200' else 0) for x in result),'real scraped metric scope/value mismatch')
                if time.monotonic()>deadline:raise Blocked('scrape responded after 30s acceptance window')
                break
            if time.monotonic()>=deadline:raise Blocked('owned signal did not appear in real Prom scrape within 30s')
            time.sleep(1)
        self.case_marker('ingress.real_signal',dict(signal_id=self.signal_id,run_id=self.run_id,real_scraped_series=2,local_capture_possible=True,external_notifications=False,synthetic_current_workload=True,historical_or_production_proof=False))
    def receiver_proof(self,stage):
        self.frozen();info=readonly.inspect_pg(self.data['components']['alertmanager']['name'])
        component_guard('alertmanager',info,self.data['components']['alertmanager'])
        source_path=ROOT/'tools/testlab/components/alertmanager.yml';source=source_fd_snapshot(source_path)
        actual=container_receiver_bytes(info['Id'])
        responses=[wire(port,'GET',route) for port,route in ((18570,'/api/v1/status/config'),(18571,'/api/v2/status'),(18571,'/metrics'))]
        check(all(status==200 for status,_ in responses),'receiver config/status/load proof unavailable')
        praw,araw,mraw=[raw for _,raw in responses]
        readonly._private(self.work/('receiver-load-'+stage+'-observation.json'),dict(source=source,actual_container_config=actual.decode(),container=info,metrics=mraw.decode(),prometheus=json.loads(praw),alertmanager=json.loads(araw),passed=False))
        proof=validate_receiver_load(info,source,actual,mraw.decode(),json.loads(praw)['data']['yaml'],json.loads(araw)['config']['original'],self.data['helper_sha256']['tools/testlab/components/alertmanager.yml'],expected=self.receiver_load)
        if source_fd_snapshot(source_path)!=source:raise Blocked('receiver source FD/path drift after load/status observation')
        final_info=readonly.inspect_pg(info['Id']);component_guard('alertmanager',final_info,self.data['components']['alertmanager'])
        if any(final_info.get(key)!=info.get(key) for key in ('Path','Args')) or final_info['State'].get('StartedAt')!=info['State'].get('StartedAt'):raise Blocked('receiver container execution identity drift after load observation')
        final_status,final_metrics=wire(18571,'GET','/metrics')
        check(final_status==200,'receiver final successful load observation unavailable')
        validate_receiver_load(final_info,source,actual,final_metrics.decode(),json.loads(praw)['data']['yaml'],json.loads(araw)['config']['original'],self.data['helper_sha256']['tools/testlab/components/alertmanager.yml'],expected=proof['binding'])
        self.receiver_load=proof['binding'];self.local_delivery=proof['local_delivery']
        readonly._private(self.work/('receiver-load-'+stage+'.json'),dict(proof=proof,source=source,actual_container_config=actual.decode(),container=info,metrics=mraw.decode(),final_metrics=final_metrics.decode(),prometheus=json.loads(praw),alertmanager=json.loads(araw)))
    def configure_sources(self):
        for name in ('push-token','push-hmac'):readonly.private_file(self.work/name,secrets.token_urlsafe(32)+'\n')
        pull=dict(sources=[dict(id=self.source_id,name='Controlled ingress metric',application_id=self.app,environment_id='test',connector_id=self.connector,target_id=self.target,resource_id=self.resource_id,query_id='controlled_signal',expression=self.expression,name_labels=['service'],label_keys=['status','signal_id'])])
        push=dict(public_host='',sources=[dict(id=self.source_id,provider='generic',application_id=self.app,environment_id='test',token_file=str(self.work/'push-token'),hmac_secret_file=str(self.work/'push-hmac'),types=list(TYPES),rate_per_minute=120)])
        for name,value in (('pull.json',pull),('push.json',push)):readonly.private_file(self.work/name,json.dumps(value))
        self.config_hashes={name:digest(self.work/name) for name in ('pull.json','push.json','push-token','push-hmac')}
        self.state.update(source_configuration_sha256=self.config_hashes,signal_id=self.signal_id,source_id=self.source_id,application_id=self.app,connector_id=self.connector,target_id=self.target)
        self.frozen();self.stop_owned_generation();self.start();self.pull_path='/api/v1/ingest/pull/'+self.source_id
        self.case_marker('ingress.private_source_configuration',dict(generations=self.generation,config_sha256=self.config_hashes,main_runtime_modified=False))
    def pull_call(self,user,test=False,index=0,scenario='normal',want=200,code=None):
        validate=validate_test if test else lambda v:validate_preview(v,self)
        before=len(self.relay.receipts)
        value,_=self.call(user,'POST' if test else 'GET',self.pull_path+('/test' if test else ''),{} if test else None,index=index,scenario=scenario,want=want,code=code,validate=validate if want==200 else None)
        count=0
        if want in (200,502):
            check(len(self.relay.receipts)==before+1,'Pull did not issue exactly one bounded source request')
            receipt=self.relay.receipts[-1]
            if want==502:check(receipt['status']==503 and receipt['injected'] is True,'failure was not the explicitly injected source503')
            else:
                check(receipt['status']==200 and receipt['injected'] is False,'normal Pull did not read actual source')
                raw=json.loads(receipt['body']);series=raw.get('data',{}).get('result',[])
                check(raw.get('status')=='success' and raw.get('data',{}).get('resultType')=='matrix' and len(series)==2 and all(x['metric'].get('signal_id')==self.signal_id for x in series),'raw real Prom range population differs')
                count=sum(len(item['values']) for item in series);check(count>0,'source returned empty range population')
                if test:check(value['rows']==count,'Pull test row count differs from exact upstream response')
                else:check(str(count)+' converted rows' in {c['label'] for c in value['preview']['checks']},'Pull preview count differs from exact upstream response')
            self.pull_expected.append(dict(actor=self.users[user] if user!='admin' else self.admin_id,kind='test' if test else 'preview',status='success' if want==200 else 'failure',rows=count,error='' if want==200 else 'upstream_failed'))
        return value
    def normal_paths(self):
        self.admin_id=self.sql('SELECT id FROM users WHERE email='+readonly.quote(self.admin_email),read=True)
        self.pull_call('reader');self.case_marker('ingress.pull.preview',dict(nonempty=True,source=self.source_id,raw_range_receipts=True))
        self.pull_call('admin',test=True,index=1);self.case_marker('ingress.pull.test',dict(nonempty=True,source=self.source_id))
        self.call('peer','GET','/api/v1/ingest/push',scenario='fixture',validate=validate_push_counts)
        for typ in TYPES:
            value,_=self.call('peer','POST','/api/v1/ingest/push/test',{'type':typ},want=202,index=1,validate=lambda v:check(v.get('code')==202 and type(v.get('receipt_id')) is int and v['receipt_id']>0,'Push test durable receipt absent'))
            self.receipts[typ]=value['receipt_id']
        check(len(set(self.receipts.values()))==5,'five Push types reused receipt identity')
        self.call('reader','GET','/api/v1/ingest/push',scenario='fixture',validate=validate_push_counts)
        self.case_marker('ingress.push.five_types',dict(receipt_ids=self.receipts,events_synthetic=True,real_today_count=0,external_vendor_proof=False))
    def ledger_counts(self):
        return json.loads(self.sql("SELECT json_build_object('attempts',(SELECT count(*) FROM v3_pull_attempts),'events',(SELECT count(*) FROM v3_push_events),'receipts',(SELECT count(*) FROM v3_push_receipts),'audit',(SELECT count(*) FROM management_audit))",read=True))
    def rejection_paths(self):
        before=self.ledger_counts();upstream=len(self.relay.receipts)
        for method,path,body in (('GET',self.pull_path,None),('POST',self.pull_path+'/test',{}),('POST','/api/v1/ingest/push/test',{'type':'change'})):
            self.call(None,method,path,body,want=401,code='unauthorized',scenario='authorization')
        self.call('reader','POST',self.pull_path+'/test',{},want=403,code='forbidden',scenario='authorization')
        self.call('reader','POST','/api/v1/ingest/push/test',{'type':'change'},want=403,code='forbidden',scenario='authorization')
        self.call('hidden','GET',self.pull_path,environment='staging',want=404,code='source_not_found',scenario='authorization')
        self.call('reader','GET',self.pull_path,environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('denied','GET',self.pull_path,want=404,code='source_not_found',scenario='authorization')
        for body in (None,{'mapping':None},{'mapping':{'name_label':'unknown','label_keys':[]}},{'mapping':{'name_label':'service','label_keys':['authorization']}},{'expression':'up'}):
            self.call('admin','POST',self.pull_path+'/test',body,want=422,code='invalid_mapping',scenario='boundary')
        for body in ({'type':'bad'},{'type':'change','extra':'x'},{}):self.call('admin','POST','/api/v1/ingest/push/test',body,want=422,code='invalid_body',scenario='boundary')
        self.call('admin','POST',self.pull_path+'/test',{},environment='all',want=409,code='env_view_only',scenario='boundary')
        self.call('admin','POST','/api/v1/ingest/push/test',{'type':'change'},environment='all',want=409,code='env_view_only',scenario='boundary')
        check(self.ledger_counts()==before and len(self.relay.receipts)==upstream,'denied/boundary request wrote state or queried source')
        self.case_marker('ingress.authorization_boundary',dict(state_unchanged=True,source_requests=0))
    def failure_recovery(self):
        with self.relay.lock:self.relay.failed=True
        try:self.pull_call('reader',want=502,code='upstream_failed',scenario='failure')
        finally:
            with self.relay.lock:self.relay.failed=False
        self.pull_call('reader',index=1)
        self.case_marker('ingress.pull.failure_recovery',dict(fault='owned fixed-query relay503; actual Prom not stopped',failure_attempt_audited=True,recovery_real_nonempty=True))
    def budget(self):
        # Fresh peer has no Pull attempts. The 11th within the real minute
        # must reject before source IO or pending-attempt/audit creation.
        started=time.monotonic()
        for i in range(10):self.pull_call('peer',scenario='boundary',index=i%2)
        if time.monotonic()-started>=55:raise Blocked('real 60s Pull budget proof requires ten requests within 55s; no clock/window manipulation')
        before=self.ledger_counts();upstream=len(self.relay.receipts)
        self.call('peer','GET',self.pull_path,want=429,code='rate_limited',scenario='boundary',index=1)
        check(self.ledger_counts()==before and len(self.relay.receipts)==upstream,'budget rejection allocated attempt/audit or reached source')
        self.case_marker('ingress.pull.budget',dict(actor_attempts=10,eleventh=429,source_queries_on_rejection=0,elapsed_seconds=time.monotonic()-started,source60_concurrent4_limits_verified=False))
    def revocation(self):
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+readonly.quote(self.roles['peer'])+" AND module='connectors' AND action='manage' AND scope_kind='environment' AND scope_id='test';")
        before=self.ledger_counts();upstream=len(self.relay.receipts)
        self.call('peer','POST',self.pull_path+'/test',{},want=403,code='forbidden',scenario='authorization',index=1)
        self.call('peer','POST','/api/v1/ingest/push/test',{'type':'change'},want=403,code='forbidden',scenario='authorization')
        check(before==self.ledger_counts() and upstream==len(self.relay.receipts),'revoked management grant still produced/read state')
        self.case_marker('ingress.revocation',dict(cross_instance=True,state_unchanged=True,slow_io_or_lock_wait_revocation_verified=False))
    def audit_consistency(self):
        attempts=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('actor',actor_id,'source',source_id,'kind',kind,'status',status,'rows',rows_count,'dropped',dropped_count,'error',error_code,'completed',completed_at)),'[]'::json) FROM v3_pull_attempts",read=True))
        audits=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('actor',actor_id,'actor_kind',actor_kind,'application',application_id,'action',action,'target',target,'outcome',outcome,'changes',changes)),'[]'::json) FROM management_audit WHERE action LIKE 'v3.ingest_pull.%' OR action LIKE 'v3.push.%'",read=True))
        push=json.loads(self.sql("SELECT json_build_object('events',COALESCE((SELECT json_agg(to_jsonb(e)) FROM v3_push_events e),'[]'::json),'receipts',COALESCE((SELECT json_agg(to_jsonb(r)) FROM v3_push_receipts r),'[]'::json),'fences',COALESCE((SELECT json_agg(to_jsonb(i)) FROM v3_push_idempotency i),'[]'::json),'bindings',COALESCE((SELECT json_agg(to_jsonb(b)) FROM v3_push_source_bindings b),'[]'::json))",read=True))
        validate_ledger(self,attempts,audits,push)
        self.receiver_proof('acceptance-end')
        readonly._private(self.work/'ingress-ledger-proof.json',dict(attempts=attempts,audits=audits,push=push))
        self.case_marker('ingress.audit_consistency',dict(pull_attempts=len(attempts),push_synthetic_events=5,exact_ingress_audits=len(audits),no_sql_result_seed=True))
    def run(self):
        self.relay=Relay(self);self.provision();self.start();self.seed()
        for user in ('admin','reader','peer','hidden','denied'):self.login(user)
        for step in (self.source_scope,self.real_signal,self.configure_sources,self.normal_paths,self.rejection_paths,self.failure_recovery,self.budget,self.revocation,self.audit_consistency):self.frozen();step()
        self.frozen();rows=[json.loads(line) for line in (self.work/'interfaces.jsonl').read_text().splitlines()]
        normal=sorted({(r['method'],r['path']) for r in rows if r.get('registration_evidence') and (r['method'],r['path'].removeprefix('/api/v1')) in TARGETS})
        check(set(normal)=={(m,'/api/v1'+p) for m,p in TARGETS},'three ingress targets lack exact normal HTTP evidence')
        result=dict(suite=MODULE,run_id=self.run_id,positive_routes=normal,http_results=self.calls,source_tree_sha=self.data['source_tree_sha'],binary_sha256=self.data['binary_sha256'],source_id=self.source_id,signal_id=self.signal_id,components=self.data['components'],fixture=self.data['fixture'],helper_sha256=self.data['helper_sha256'],config_sha256=self.config_hashes,local_notification_side_effect=self.local_delivery,unverified=['vendor/external generic signed ingress','real production/history/continuous sampling','Pull source60/concurrent4 budgets','crash pending-attempt recovery','slow-read/queued-lock session or grant revoke','external credentials and TLS source failures','API client response loss after durable commit'],formal_performance_acceptance=False,formal_24h_acceptance=False)
        readonly._private(self.work/'ingress-result.json',result);return result
    def cleanup(self):
        first=None
        try:super().cleanup()
        except BaseException as error:first=error
        if self.relay:
            try:
                self.relay.close();readonly._private(self.evidence_work/'relay-cleanup.json',dict(port=RELAY_PORT,stopped=True,shared_signal_or_capture_deleted=False,source_components_stopped=False))
                emit('ingress.relay_cleanup','passed',dict(port=RELAY_PORT,shared_signal_or_capture_deleted=False))
            except BaseException as error:
                if first is None:first=error
                else:
                    try:readonly._private(self.evidence_work/'relay-cleanup-error.json',dict(error_type=type(error).__name__))
                    except BaseException:pass
        if first is not None:raise first


def validate_ledger(chain,attempts,audits,push):
    expected=chain.pull_expected
    check(len(attempts)==len(expected) and all(x['source']==chain.source_id and x['completed'] and x['dropped']==0 for x in attempts),'Pull pending/wrong-source/unexpected attempts exist')
    check(sorted((x['actor'],x['kind'],x['status'],x['error'],x['rows']) for x in attempts)==sorted((x['actor'],x['kind'],x['status'],x['error'],x['rows']) for x in expected),'Pull outcome/actor/kind/exact upstream rows differs from actual HTTP sequence')
    pull_audits=[x for x in audits if x['action'].startswith('v3.ingest_pull.')]
    check(len(pull_audits)==len(attempts),'Pull attempt/audit cardinality differs')
    actual=[]
    for row in pull_audits:
        c=row['changes'];check(row['actor_kind']=='user' and row['target']==chain.source_id and row['application']==chain.app and c['source_id']==chain.source_id and c['environment_id']=='test','Pull audit exact actor/application/environment/source mismatch')
        check(row['action']=='v3.ingest_pull.'+c['kind'] and c['dropped']==0,'Pull audit action/count mismatch')
        actual.append((row['actor'],c['kind'],row['outcome'],c['error_code'],c['rows']))
    check(sorted(actual)==sorted((x['actor'],x['kind'],x['status'],x['error'],x['rows']) for x in attempts),'Pull audit row count/outcome differs from durable attempts')
    check(all(x['rows']>0 if x['status']=='success' else x['rows']==0 for x in attempts),'Pull successful rows empty or failed rows persisted')
    check(len(push['events'])==len(push['receipts'])==len(push['fences'])==5 and len(push['bindings'])==1,'synthetic Push event/receipt/fence/binding cardinality differs')
    binding=push['bindings'][0];check(binding['source_id']==chain.source_id and binding['application_id']==chain.app and binding['environment_id']=='test' and binding['provider']=='generic','Push source binding differs')
    push_audits=[a for a in audits if a['action'].startswith('v3.push.')];check(len(push_audits)==5,'Push test audit count differs')
    for typ,receipt_id in chain.receipts.items():
        receipts=[r for r in push['receipts'] if r['id']==receipt_id];events=[e for e in push['events'] if e['event_type']==typ]
        matching=[a for a in push_audits if a['changes']['receipt_id']==receipt_id]
        check(len(receipts)==len(events)==len(matching)==1,'Push HTTP receipt/type/audit identity ambiguous')
        receipt,event,audit=receipts[0],events[0],matching[0]
        for row in (receipt,event):check(row['source_id']==chain.source_id and row['provider']=='generic' and row['application_id']==chain.app and row['environment_id']=='test' and row['event_type']==typ and row['synthetic'] is True,'Push event/receipt escaped source/synthetic scope')
        check(receipt['code']==202 and receipt['result']=='test_accepted' and event['subject']=='push-test' and event['state']==dict(alert='firing',change='succeeded',incident='open',resource='updated',oncall='started')[typ],'synthetic Push durable semantics differ')
        fences=[f for f in push['fences'] if f['event_id']==event['id']]
        check(len(fences)==1 and fences[0]['source_id']==chain.source_id and fences[0]['event_type']==typ and fences[0]['key_hash']==receipt['key_hash'],'Push idempotency fence does not match event/receipt')
        c=audit['changes'];check(audit['actor']==chain.users['peer'] and audit['actor_kind']=='user' and audit['application']==chain.app and audit['target']==chain.source_id and audit['action']=='v3.push.test_accepted' and audit['outcome']=='test_accepted' and c['source_id']==chain.source_id and c['environment_id']=='test' and c['event_type']==typ,'Push audit actor/source/app/env/type/receipt differs')


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT)
    mode=parser.add_mutually_exclusive_group(required=True);mode.add_argument('--live',action='store_true');mode.add_argument('--inventory',action='store_true');args=parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(dict(module=MODULE,normal_targets=[m+' /api/v1'+p for m,p in TARGETS],required_cases=REQUIRED_CASES,ports=[*readonly.PORTS,RELAY_PORT],components=COMPONENTS,offline_only=True,local_capture_may_append=True,product_frontend_or_native_cicd=False),ensure_ascii=False));return 0
    original=readonly.emit;readonly.emit=emit
    try:
        with readonly.exclusive(args.root),readonly.cooperative_signals():
            data=doctor(args.root);emit('ingress.doctor','passed',dict(components=data['components'],fixture=data['fixture'],source_tree_sha=data['source_tree_sha']))
            readonly.execute(Chain(args.root,data,str(uuid.uuid4())))
    except Blocked as error:emit('ingress.environment','blocked',dict(reason=str(error)));return 2
    except BaseException as error:emit('ingress.failure','interrupted' if isinstance(error,(KeyboardInterrupt,SystemExit)) else 'failed',dict(error_type=type(error).__name__));return 1
    finally:readonly.emit=original
    return 0


if __name__=='__main__':raise SystemExit(main())

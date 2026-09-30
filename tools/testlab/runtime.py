"""Reviewed local rule-source/trial isolation and TLS reverse proxies."""
from __future__ import annotations
import hashlib, hmac, json, os, secrets, ssl, subprocess, threading, urllib.error, urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from env import _cmd, _private, _inspect, LABEL, prepare

def _generated_json(path):
    path=Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o777 != 0o600:
        raise RuntimeError('generated runtime fixture requires a regular 0600 file')
    return json.loads(path.read_text())

def configure_consumers(root):
    """Add native consumers without touching an existing trial/perf hook."""
    root=Path(root).resolve();environment=prepare(root)
    components=_generated_json(root/'.local/testlab/component-environment.json')
    runtime=_generated_json(root/'.local/testlab/runtime-config.json')
    backend=_generated_json(root/'.local/testlab/backend-fixture.json')
    owner=environment['owner'];work=Path(environment['work'])
    if any(record.get('owner')!=owner for record in (components,runtime,backend)) or Path(components.get('work','')).resolve()!=work:
        raise RuntimeError('native consumer fixture ownership mismatch')
    if backend.get('database_url')!=environment['env']['TESTLAB_APP_DSN'] or backend.get('admin',{}).get('email')!='admin@testlab.invalid':
        raise RuntimeError('native consumer database/account mismatch')
    pg=_inspect(environment['container'])
    if not pg or not pg['State']['Running'] or pg['Config'].get('Labels',{}).get(LABEL)!=owner:
        raise RuntimeError('native consumer PG owner unavailable')
    endpoints={'prometheus':18570,'loki':18573,'tempo':18574}
    for component,port in endpoints.items():
        endpoint=f'http://127.0.0.1:{port}'
        registered=[s for s in components['services'] if s['name']==component]
        if len(registered)!=1 or registered[0].get('url')!=endpoint or registered[0].get('mode')!='real_local' or registered[0].get('container')!='aiops-'+owner+'-'+component or components['host_config'].get(component+'_url')!=endpoint:
            raise RuntimeError('native consumer endpoint registration mismatch')
        state=_inspect(registered[0]['container'])
        if not state or not state['State']['Running'] or state['Config'].get('Labels',{}).get(LABEL)!=owner or not any(b.get('HostIp')=='127.0.0.1' and b.get('HostPort')==str(port) for bindings in state['NetworkSettings']['Ports'].values() for b in (bindings or [])):
            raise RuntimeError('native consumer live port ownership mismatch')
        if component=='loki':
            loki_config=root/'tools/testlab/components/loki.yml'
            if not any(m.get('Source')==str(loki_config) and m.get('Destination')=='/etc/testlab-config.yml' and not m.get('RW') for m in state.get('Mounts',[])) or 'max_query_lookback: 24h' not in loki_config.read_text() or components['host_config'].get('loki_pattern_field')!='message_template' or components['host_config'].get('loki_complete_lookback_seconds')!=86400:
                raise RuntimeError('local Loki lookback/template configuration unverified')
    # Native HTTP binds only the reserved generated workload. Existing bindings
    # are retained, and conflicting reserved IDs or telemetry scopes fail closed.
    sql="""BEGIN;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM users WHERE email='admin@testlab.invalid' AND NOT disabled)
 OR NOT EXISTS(SELECT 1 FROM applications WHERE id='testlab' AND NOT archived)
 OR NOT EXISTS(SELECT 1 FROM application_environments WHERE id='testlab-test' AND application_id='testlab' AND key='test' AND NOT archived)
 THEN RAISE EXCEPTION 'reserved native testlab scope unavailable'; END IF;
 IF EXISTS(SELECT 1 FROM application_environments e, jsonb_to_recordset(e.bindings) AS b(kind text,connector_id text,resource text)
 WHERE e.id='testlab-test' AND b.kind IN ('loki','tempo') AND (b.resource IS DISTINCT FROM 'testlab-test' OR b.connector_id IS DISTINCT FROM b.kind))
 THEN RAISE EXCEPTION 'conflicting native testlab telemetry binding'; END IF;
END $$;
UPDATE application_environments e SET bindings=e.bindings||addition.items,binding_revision=e.binding_revision+1
FROM (SELECT jsonb_agg(item) items FROM jsonb_array_elements('[{"kind":"loki","connector_id":"loki","resource":"testlab-test"},{"kind":"tempo","connector_id":"tempo","resource":"testlab-test"}]'::jsonb) item
 WHERE NOT EXISTS(SELECT 1 FROM application_environments e, jsonb_array_elements(e.bindings) existing WHERE e.id='testlab-test' AND existing->>'kind'=item->>'kind')) addition
WHERE e.id='testlab-test' AND addition.items IS NOT NULL;
INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools)
VALUES('testlab-native-prometheus','test','prometheus','Testlab native Prometheus','observe','http://127.0.0.1:18570','none',true,'[{"name":"query_metrics","enabled":true}]') ON CONFLICT(id) DO NOTHING;
-- Repair only this owner's previously generated invalid catalog value.
UPDATE v3_connectors SET category='observe'
WHERE id='testlab-native-prometheus' AND environment_id='test' AND provider='prometheus'
 AND endpoint='http://127.0.0.1:18570' AND auth_method='none' AND credential_ref='' AND enabled
 AND tools='[{"name":"query_metrics","enabled":true}]'::jsonb AND category='observability';
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM v3_connectors WHERE id='testlab-native-prometheus' AND environment_id='test' AND provider='prometheus' AND category='observe' AND endpoint='http://127.0.0.1:18570' AND auth_method='none' AND credential_ref='' AND enabled AND tools='[{"name":"query_metrics","enabled":true}]'::jsonb)
 THEN RAISE EXCEPTION 'conflicting native testlab connector'; END IF;
END $$;
INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
SELECT 'testlab-native-metrics','testlab','test','testlab-native-prometheus','metric_resource','testlab-metrics',id FROM users WHERE email='admin@testlab.invalid' ON CONFLICT(id) DO NOTHING;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM v3_connector_targets WHERE id='testlab-native-metrics' AND application_id='testlab' AND environment_id='test' AND connector_id='testlab-native-prometheus' AND target_kind='metric_resource' AND external_id='testlab-metrics')
 THEN RAISE EXCEPTION 'conflicting native testlab metric target'; END IF;
END $$;
COMMIT;"""
    _cmd(['docker','exec','-i',environment['container'],'psql','-U','testlab','-d','aiops_testlab_app','-v','ON_ERROR_STOP=1'],input=sql)
    directory=work/'native-consumers'
    expression='sum(rate(testlab_requests_total{application="testlab",environment="test"}[1m]))'
    telemetry=directory/'telemetry.json';catalog=directory/'prometheus-queries.json'
    _private(telemetry,{'prometheus':{'url':'http://127.0.0.1:18570','allow_http':True},
          'loki':{'url':'http://127.0.0.1:18573','allow_http':True},'tempo':{'url':'http://127.0.0.1:18574','allow_http':True},
          'tempo_search_fail_closed':False,'applications':{'testlab-test':{'prometheus_queries':{'requests':expression},
          'loki_labels':{'application':'testlab','environment':'test','testlab_owner':owner},
          'loki_pattern_field':'message_template','loki_complete_lookback_seconds':86400,
          'tempo_service':'testlab-api','tempo_attributes':{'application':'testlab','environment':'test'},
          'tempo_safe_operations':['GET /controlled-signal','GET /native-testlab']}}})
    _private(catalog,{'queries':[{'connector_id':'testlab-native-prometheus','application_id':'testlab','environment_id':'test',
          'resource_id':'testlab-metrics','query_id':'request_rate','expression':expression}]})
    runtime['env'].update({'AIOPS_TELEMETRY_CONFIG':str(telemetry),'AIOPS_V3_PROMETHEUS_QUERIES_CONFIG':str(catalog)})
    runtime['native_consumers']={'application_id':'testlab','environment_id':'test','resource_id':'testlab-test',
         'metrics_query_id':'request_rate','data_scope':'controlled current workload; no historical population or Tempo search-completeness attestation'}
    _private(root/'.local/testlab/runtime-config.json',runtime)
    return runtime

def configure(root):
    root=Path(root);environment=prepare(root)
    components=json.loads((root/'.local/testlab/component-environment.json').read_text())
    if components['owner']!=environment['owner']:raise RuntimeError('component owner mismatch')
    work=Path(environment['work'])/'trial';work.mkdir(exist_ok=True,mode=0o700)
    for role in ('source','trial','webhook'):
        token=work/(role+'-token')
        if not token.exists():
            token.write_text(secrets.token_hex(32));os.chmod(token,0o600)
    config=work/'prometheus.yml'
    config.write_text('global:\n  scrape_interval: 5s\n  evaluation_interval: 5s\nscrape_configs:\n  - job_name: single-application-testlab\n    static_configs:\n      - targets: [host.docker.internal:18578]\n')
    name='aiops-'+environment['owner']+'-trial-prometheus'
    inspected=subprocess.run(['docker','inspect',name],text=True,capture_output=True)
    if inspected.returncode==0:
        info=json.loads(inspected.stdout)[0]
        if info['Config'].get('Labels',{}).get(LABEL)!=environment['owner']:raise RuntimeError('trial Prometheus owner mismatch')
        if not info['State']['Running']:_cmd(['docker','start',name])
    else:
        _cmd(['docker','run','-d','--name',name,'--label',LABEL+'='+environment['owner'],'--memory','512m','--cpus','1',
              '-p','127.0.0.1:18567:9090','-v',str(config)+':/etc/prometheus/prometheus.yml:ro',
              'prom/prometheus:v3.13.3','--config.file=/etc/prometheus/prometheus.yml','--storage.tsdb.retention.time=2d'],timeout=90)
    # Only generated data in the owned application database. The testlab
    # application is reserved for actual local metrics and never archived here.
    sql="""BEGIN;
INSERT INTO applications(id,name,description,environment,key) VALUES('testlab','Testlab','Synthetic local workload','test','testlab') ON CONFLICT(id) DO NOTHING;
INSERT INTO application_environments(id,application_id,key,name) VALUES('testlab-test','testlab','test','Test') ON CONFLICT(id) DO NOTHING;
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('testlab','test') ON CONFLICT DO NOTHING;
COMMIT;"""
    _cmd(['docker','exec','-i',environment['container'],'psql','-U','testlab','-d','aiops_testlab_app','-v','ON_ERROR_STOP=1'],input=sql)
    source=work/'rules.json';trial=work/'trial.json'
    _private(source,{'environments':{'test':{'url':'https://localhost:18564','bearer_token_file':str(work/'source-token')}},
                    'bindings':[{'environment_id':'test','application_id':'testlab','file':'/etc/prometheus/testlab-rules.yml','group':'testlab-rules'}]})
    _private(trial,{'endpoints':[{'environment_id':'test','application_id':'testlab','url':'https://localhost:18565',
                  'bearer_token_file':str(work/'trial-token'),'isolation_attested':True,
                  'isolation_reviewed_by':'local-testlab-source-inspection','isolation_reviewed_at':datetime.now(timezone.utc).isoformat().replace('+00:00','Z')}]})
    operations=work/'operations.json'
    _private(operations,{'sources':[{'id':'testlab-periodic','token_file':str(work/'webhook-token'),
             'mappings':[{'labels':{'application':'testlab','environment':'test'},'application_id':'testlab','environment_id':'testlab-test'}]}],
             'feishu_channels':[]})
    # Review evidence: no remote_write/federation/additional scrape sources;
    # fresh dedicated container accepts only the one testlab metrics source.
    data={'owner':environment['owner'],'source_config_sha':hashlib.sha256(config.read_bytes()).hexdigest(),
          'trial_container':name,'work':str(work),'ca_file':components['host_config']['ca_file'],
          'tls_cert':str(Path(components['work'])/'tls-cert.pem') if 'work' in components else components['host_config']['ca_file'],
          'tls_key':str(Path(components.get('work',Path(components['host_config']['ca_file']).parent))/'tls-key.pem'),
          'env':{'AIOPS_V3_ALERT_RULES_CONFIG':str(source),'AIOPS_V3_ALERT_RULE_TRIAL_CONFIG':str(trial),
                 'AIOPS_OPERATIONS_CONFIG':str(operations),'SSL_CERT_FILE':components['host_config']['ca_file']},
          'proxies':[{'port':18564,'upstream':'http://127.0.0.1:18570','token_file':str(work/'source-token')},
                     {'port':18565,'upstream':'http://127.0.0.1:18567','token_file':str(work/'trial-token')}],
          'attestation_scope':'fresh dedicated local single-application deployment; not a production isolation proof'}
    _private(root/'.local/testlab/runtime-config.json',data)
    perf_work=Path(environment['work'])/'perf'
    # Preserve the verified completed trial ID/status and existing perf hook.
    if not (perf_work/'soak-hooks.json').exists():_private(perf_work/'soak-hooks.json',{
      'dryrun':{'account':'admin','method':'POST','path':'/api/v1/alert-rules','expected_status':202,
                'headers':{'Idempotency-Key':'testlab-continuous-trial-20260930'},
                'body':{'application_id':'testlab','environment_id':'test','group':'testlab-rules','name':'TestlabContinuousTrial',
                        'expr':'testlab_request_failures_total{application="testlab",environment="test"} > 0','for':'0s',
                        'labels':{'application':'testlab','environment':'test','severity':'P3'},
                        'annotations':{'summary':'Synthetic local continuous trial'},'mode':'dry_run'}},
      'webhook':{'method':'POST','path':'/api/v1/webhooks/alertmanager/testlab-periodic','expected_status':202,
                 'headers':{'Authorization':'Bearer '+(work/'webhook-token').read_text().strip()},
                 'body':{'alerts':[{'status':'firing','fingerprint':'testlab-continuous-webhook-20260930',
                                  'startsAt':datetime.now(timezone.utc).isoformat().replace('+00:00','Z'),
                                  'labels':{'application':'testlab','environment':'test','alertname':'TestlabPeriodic','severity':'warning'},
                                  'annotations':{'summary':'Synthetic repeat delivery'}}]}},
      'interval_seconds':60,'trial_expected_statuses':['queued','running','completed']})
    return configure_consumers(root)

def start_proxies(data):
    servers=[]
    for item in data.get('proxies',[]):
        def factory(binding):
            class Proxy(BaseHTTPRequestHandler):
                protocol_version='HTTP/1.1'
                def log_message(self,*args):pass
                def do_GET(self):self.forward('GET')
                def do_POST(self):
                    # Prometheus query/format APIs use POST forms but remain
                    # read-only. Never proxy admin, reload, or deletion routes.
                    if self.path.split('?',1)[0] not in ('/api/v1/format_query','/api/v1/query','/api/v1/query_range'):
                        self.send_error(404);return
                    self.forward('POST')
                def forward(self,method):
                    token=Path(binding['token_file']).read_text().strip()
                    if not hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+token):
                        self.send_error(401);return
                    if not self.path.startswith('/api/v1/') or len(self.path)>16384:
                        self.send_error(404);return
                    payload=None;headers={}
                    if method=='POST':
                        try:length=int(self.headers.get('Content-Length','0'))
                        except ValueError:self.send_error(400);return
                        if length<0 or length>65536 or self.headers.get('Transfer-Encoding'):
                            self.send_error(413);return
                        if self.headers.get('Content-Type','').split(';',1)[0]!='application/x-www-form-urlencoded':
                            self.send_error(415);return
                        payload=self.rfile.read(length);headers['Content-Type']='application/x-www-form-urlencoded'
                    try:
                        request=urllib.request.Request(binding['upstream']+self.path,data=payload,method=method,headers=headers)
                        with urllib.request.urlopen(request,timeout=20) as r:
                            body=r.read((2<<20)+1);code=r.status;content=r.headers.get('Content-Type','application/json')
                        if len(body)>2<<20:raise ValueError('response too large')
                    except urllib.error.HTTPError as e:body=e.read(8192);code=e.code;content='application/json'
                    except Exception:self.send_error(503);return
                    self.send_response(code);self.send_header('Content-Type',content);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            return Proxy
        server=ThreadingHTTPServer(('127.0.0.1',item['port']),factory(item))
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(data['tls_cert'],data['tls_key'])
        server.socket=context.wrap_socket(server.socket,server_side=True)
        threading.Thread(target=server.serve_forever,daemon=True).start();servers.append(server)
    return servers

if __name__=='__main__':
    root=Path(__file__).resolve().parents[2]
    try:
        d=configure(root);print(json.dumps({'status':'passed','name':'本地独立应用试运行准备','actual':d['attestation_scope'],'evidence_mode':'real_local'}))
    except Exception as e:print(json.dumps({'status':'blocked','name':'本地试运行准备','actual':type(e).__name__+': '+str(e)}));raise SystemExit(2)

"""Offline isolation/protocol regressions. Never starts Docker, PG, HTTP or Go."""
import contextlib
import copy
import hashlib
import hmac
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_native_projection_coverage as projection
from inventory import actual_interface


class ProjectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        binary = self.root / '.local/testlab/environments' / projection.OWNER / 'backend/aiops'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'owned nonrace source fixture')
        self.data = dict(owner=projection.OWNER, binary=str(binary), source_tree_sha='frozen-source',
            binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
            postgres_container='aiops-'+projection.OWNER+'-pg', postgres_image_id='sha256:pg',
            postgres_client_addresses=['127.0.0.1', '::1'], control_secret='generated-private-secret')
        self.contract = projection.Contract(projection.ROOT)
        with patch.object(projection, 'Contract', return_value=self.contract):
            self.chain = projection.Chain(self.root, self.data, str(uuid.uuid4()))

    def pg_info(self):
        return dict(Name='/aiops-'+projection.OWNER+'-pg', Image='sha256:pg',
            Config=dict(Image=projection.PG_IMAGE, Labels={'aiops.testlab.owner':projection.OWNER}),
            State={'Running':True}, NetworkSettings={'Ports':{'5432/tcp':[{'HostIp':'127.0.0.1','HostPort':'15461'}]}})

    def offline_start_controls(self, emitted, responses=None):
        stack=contextlib.ExitStack();self.addCleanup(stack.close)
        sock=Mock();sock.connect_ex.return_value=1
        factory=stack.enter_context(patch.object(projection.socket,'socket'))
        factory.return_value.__enter__.return_value=sock
        process=Mock(pid=12345);process.poll.return_value=None
        stack.enter_context(patch.object(projection.subprocess,'Popen',return_value=process))
        stack.enter_context(patch.object(projection,'command_env',return_value={}))
        stack.enter_context(patch.object(projection,'_process_port',return_value=True))
        stack.enter_context(patch.object(self.chain,'frozen'))
        stack.enter_context(patch.object(projection,'transport',side_effect=responses,return_value=(200,b'{}',{})))
        stack.enter_context(patch.object(projection,'emit',side_effect=lambda *args,**kwargs:emitted.append((args,kwargs))))

    def test_two_original_start_flows_have_unique_runner_readiness_keys(self):
        emitted=[];self.offline_start_controls(emitted)
        self.chain.start();self.chain.start()
        names=[args[0] for args,kwargs in emitted]
        keys=[(projection.MODULE,name) for name in names]
        self.assertEqual(len(keys),len(set(keys)),'Runner immutable attempt key must never repeat')
        self.assertEqual(names,['projection.api_ready.0.0','projection.api_ready.0.1','projection.api_ready.1.0','projection.api_ready.1.1'])
        self.assertEqual(sorted(path.name for path in self.chain.work.glob('api-*.log')),['api-0-0.log','api-0-1.log','api-1-0.log','api-1-1.log'])

    def test_partial_start_cancel_or_error_consumes_generation_before_retry(self):
        for failure in (KeyboardInterrupt(),RuntimeError('offline failed second child')):
            with self.subTest(failure=type(failure).__name__):
                # Separate fixture for each failure, with real private log writes.
                with patch.object(projection,'Contract',return_value=self.contract):
                    chain=projection.Chain(self.root,self.data,str(uuid.uuid4()))
                saved=self.chain;self.chain=chain;emitted=[]
                try:
                    self.offline_start_controls(emitted,[(200,b'{}',{}),failure,(200,b'{}',{}),(200,b'{}',{})])
                    with self.assertRaises(type(failure)) as caught:chain.start()
                    self.assertIs(caught.exception,failure)
                    chain.start()
                    names=[args[0] for args,kwargs in emitted]
                    self.assertEqual(names,['projection.api_ready.0.0','projection.api_ready.1.0','projection.api_ready.1.1'])
                    self.assertEqual(len(names),len(set(names)))
                finally:self.chain=saved

    def test_cooperative_sigterm_inside_call_preserves_original_and_cleans_execute(self):
        handlers={};original=[];rows=[]
        def register(sig,callback):old=handlers.get(sig);handlers[sig]=callback;return old
        def transport(*args,**kwargs):
            try:handlers[signal.SIGTERM](signal.SIGTERM,None)
            except projection.Blocked as error:original.append(error);raise
        self.chain.run=lambda:self.chain.call('reader','GET','/api/v1/service-mappings',validate=lambda v:None)
        self.chain.cleanup=Mock()
        def emit(*args,**kwargs):rows.append((args,kwargs))
        with patch.object(projection.signal,'signal',side_effect=register),patch.object(projection,'transport',side_effect=transport),patch.object(projection,'emit',side_effect=emit):
            with projection.cooperative_signals():
                with self.assertRaises(projection.Blocked) as caught:projection.execute(self.chain)
        self.assertIs(caught.exception,original[0]);self.chain.cleanup.assert_called_once()
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertEqual(row['status'],'blocked');self.assertFalse(row['registration_evidence']);self.assertIsNone(row['actual']['status'])
        self.assertEqual(row['actual']['cancellation_reason'],str(original[0]))
        self.assertNotIn('projection.complete',[args[0] for args,kw in rows])

    def test_call_cancellation_survives_first_red_append_emit_errors(self):
        for cancellation in (projection.Blocked('owned environment interrupted'),KeyboardInterrupt(),SystemExit(130)):
            for failing_report in ('first_red','append_json','emit'):
                with self.subTest(cancellation=type(cancellation).__name__,report=failing_report):
                    with patch.object(projection,'Contract',return_value=self.contract):chain=projection.Chain(self.root,self.data,str(uuid.uuid4()))
                    chain.run=lambda:chain.call('reader','GET','/api/v1/service-mappings',validate=lambda v:None)
                    chain.cleanup=Mock();red=Mock();append=Mock();emit=Mock()
                    reports={'first_red':red,'append_json':append,'emit':emit};reports[failing_report].side_effect=RuntimeError('offline evidence failure')
                    with patch.object(chain,'first_red',red),patch.object(projection,'append_json',append),patch.object(projection,'emit',emit),patch.object(projection,'transport',side_effect=cancellation):
                        with self.assertRaises(type(cancellation)) as caught:projection.execute(chain)
                    self.assertIs(caught.exception,cancellation);chain.cleanup.assert_called_once()
                    row=append.call_args.args[1]
                    self.assertEqual(row['status'],'blocked' if isinstance(cancellation,projection.Blocked) else 'interrupted')
                    self.assertFalse(row['registration_evidence']);self.assertIsNone(row['actual']['status'])
                    self.assertNotIn('projection.complete',[call.args[0] for call in emit.call_args_list])

    def test_inventory_exact_seven_main_no_environment_side_effects(self):
        with patch.object(projection, 'doctor') as doctor, patch.object(projection.subprocess, 'run') as run:
            value = projection.inventory(projection.ROOT)
        self.assertEqual(value['selected_route_count'], 7)
        self.assertEqual(sum(x['route_class']=='main' for x in value['targets']), 7)
        self.assertEqual(value['ports'], [18610,18611])
        self.assertIn('no new positive acceptance', value['proof'])
        self.assertIn('Agent runtime/importer', value['exclusions'])
        doctor.assert_not_called();run.assert_not_called()

    def test_cli_inventory_never_reads_private_environment_or_executes_live(self):
        with patch.object(projection, 'doctor') as doctor, patch.object(projection, 'execute') as execute, \
             patch.object(projection, 'private_json') as private, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(projection.main(['--inventory']), 0)
        doctor.assert_not_called();execute.assert_not_called();private.assert_not_called()

    def test_cli_no_mode_refuses_before_resource_access(self):
        with patch.object(projection, 'doctor') as doctor, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            projection.main([])
        doctor.assert_not_called()

    def test_exact_pg_owner_image_and_listener_required(self):
        info = self.pg_info();projection.guard_pg(info, projection.OWNER, 'sha256:pg')
        for item in (dict(info, Name='/other'),dict(info, Image='sha256:other'),dict(info, State={'Running':False}),
                     dict(info, Config=dict(Image=projection.PG_IMAGE, Labels={'aiops.testlab.owner':'foreign'})),
                     dict(info, NetworkSettings={'Ports':{'5432/tcp':[{'HostIp':'0.0.0.0','HostPort':'15461'}]}})):
            with self.subTest(item=item), self.assertRaises(projection.Blocked):
                projection.guard_pg(item, projection.OWNER, 'sha256:pg')

    def test_owned_work_and_credentials_private(self):
        self.assertEqual(self.chain.work.parts[-3], projection.OWNER)
        self.assertEqual(self.chain.work.parts[-2], 'api-projection')
        self.assertEqual(os.stat(self.chain.work/'credentials.json').st_mode & 0o777, 0o600)
        self.assertNotIn('business', self.chain.database)
        self.assertTrue(self.chain.database.startswith('aiops_testlab_projection_'))

    def test_nonce_hmac_wrong_nonce_and_forged_proof_refused(self):
        current = dict(owner=projection.OWNER, pid=500, api_pids=[501,502], ready=True, source_tree_sha='frozen-source')
        data = dict(self.data, supervisor_pid=500);nonce='a'*32
        receipt=dict(nonce=nonce, identity=current, proof=projection.control_proof(data['control_secret'],nonce,current))
        self.assertEqual(projection.signed_identity(data,nonce,receipt), current)
        for changed in (dict(receipt, nonce='b'*32), dict(receipt,proof='forged')):
            with self.assertRaises(projection.Blocked):projection.signed_identity(data,nonce,changed)

    def test_sql_refuses_shared_database_before_docker(self):
        with patch.object(projection, 'inspect_pg') as inspect, self.assertRaises(projection.Blocked):
            self.chain.pg('SELECT 1',database='aiops_testlab_app')
        inspect.assert_not_called()

    def test_sql_configuration_identity_seed_never_inserts_business_results(self):
        captured=[]
        def sql(statement, read=False):
            captured.append((statement,read))
            return json.dumps({'users':4,'applications':0,'business':0}) if read else ''
        with patch.object(self.chain,'sql',side_effect=sql), patch.object(projection,'emit'):
            self.chain.seed()
        writes='\n'.join(x for x,read in captured if not read)
        for table in ('applications','application_environments','ops_incidents','v3_push_events',
                      'v3_push_receipts','v3_postmortems','management_audit','v3_incident_cases'):
            self.assertNotIn('INSERT INTO '+table,writes)
            self.assertNotIn('UPDATE '+table,writes)
        self.assertIn("'business'",writes)
        self.assertNotIn("'global'",writes)

    def test_source_config_fingerprint_detects_each_file_drift(self):
        for name in self.chain.files:(self.chain.work/name).write_bytes(name.encode())
        self.chain.config_digest=hashlib.sha256(b''.join((self.chain.work/x).read_bytes() for x in self.chain.files)).hexdigest()
        with patch.object(projection,'source_fingerprint',return_value='frozen-source'):
            self.chain.frozen()
            for name in self.chain.files:
                path=self.chain.work/name;old=path.read_bytes();path.write_bytes(b'changed')
                with self.subTest(file=name),self.assertRaises(projection.Blocked):self.chain.frozen()
                path.write_bytes(old)

    def test_binary_or_unknown_source_drift_blocks(self):
        with patch.object(projection,'source_fingerprint',return_value='unknown'),self.assertRaises(projection.Blocked):self.chain.frozen()
        Path(self.data['binary']).write_bytes(b'changed')
        with patch.object(projection,'source_fingerprint',return_value='frozen-source'),self.assertRaises(projection.Blocked):self.chain.frozen()

    def test_foreign_prometheus_owner_or_listener_refused_without_request(self):
        info=dict(Name='/aiops-'+projection.OWNER+'-prometheus',Image='sha256:prom',
            Config=dict(Image='prom/prometheus:v3.13.3',Labels={'aiops.testlab.owner':projection.OWNER}),State={'Running':True},
            NetworkSettings={'Ports':{'9090/tcp':[{'HostIp':'127.0.0.1','HostPort':'18570'}]}})
        with patch.object(projection,'inspect_pg',return_value=info):
            self.assertEqual(self.chain.owned_prometheus()['url'],'http://127.0.0.1:18570')
        for changed in (dict(info,Name='/foreign'),dict(info,State={'Running':False}),
                        dict(info,NetworkSettings={'Ports':{'9090/tcp':[{'HostIp':'0.0.0.0','HostPort':'18570'}]}})):
            with patch.object(projection,'inspect_pg',return_value=changed),self.assertRaises(projection.Blocked):self.chain.owned_prometheus()

    def test_transport_refuses_shared_api_and_controller_write(self):
        with patch.object(projection.http.client,'HTTPConnection') as connection:
            for base,method,path in (('http://127.0.0.1:18561','GET','/healthz'),
                                    (projection.BASES[0],'GET','//foreign/path'),
                                    (projection.CONTROL,'POST','/stop')):
                with self.assertRaises(projection.Blocked):projection.transport(base,method,path)
        connection.assert_not_called()

    def test_original_bytes_hmac_not_reserialized_and_every_binding_matters(self):
        raw=b'{ "state" : "succeeded", "title": "local" }';stamp='1790790000';source='local-owned';key='owned-key'
        got=projection.push_signature('private-generated',stamp,source,'change',key,raw)
        expected='sha256='+hmac.new(b'private-generated',(stamp+'\n'+source+'\nchange\n'+key+'\n').encode()+raw,hashlib.sha256).hexdigest()
        self.assertEqual(got,expected)
        for changed in (raw+b' ',json.dumps(json.loads(raw)).encode()):
            self.assertNotEqual(got,projection.push_signature('private-generated',stamp,source,'change',key,changed))
        self.assertNotEqual(got,projection.push_signature('private-generated',stamp,source+'x','change',key,raw))

    def test_signed_public_call_transmits_exact_original_bytes_without_header_evidence(self):
        raw=b'{ "schema": "change.v1" }';body=json.loads(raw)
        headers={'Authorization':'Bearer private-generated','X-Signature':'sha256='+'a'*64}
        with patch.object(projection,'transport',return_value=(202,b'{"accepted":1,"receipt_id":3}',{})) as transport, \
             patch.object(projection,'emit'):
            self.chain.call(None,'POST','/api/v1/events/change',body,want=202,headers=headers,raw_body=raw,purpose='fixture')
        self.assertIs(transport.call_args.args[3],raw)
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertNotIn('private-generated',json.dumps(row))
        self.assertFalse(row['registration_evidence'])

    def test_raw_body_or_source_headers_cannot_cross_producer_boundary(self):
        with patch.object(projection,'transport') as transport:
            for method,path,kw in (('GET','/api/v1/postmortems',{'headers':{'Authorization':'Bearer secret'}}),
                                   ('POST','/api/v1/incidents',{'raw_body':b'{}'}),
                                   ('POST','/api/v1/events/change',{'raw_body':b'{"different":1}'})):
                with self.assertRaises(projection.Blocked):self.chain.call(None,method,path,{},**kw)
        transport.assert_not_called()

    def test_target_empty_200_cannot_pass_normal(self):
        with patch.object(projection,'transport',return_value=(200,b'{"items":[]}',{})),patch.object(projection,'emit'),self.assertRaises(AssertionError):
            self.chain.call('reader','GET','/api/v1/postmortems')
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertEqual(row['status'],'failed');self.assertFalse(row['registration_evidence'])

    def test_nonempty_normal_matrix_row_has_actual_status_and_canonical_path(self):
        value={'rows':[{'service':'owned','cells':{}}],'systems':[],'suggestions':0}
        with patch.object(projection,'transport',return_value=(200,json.dumps(value).encode(),{})),patch.object(projection,'emit') as emit:
            self.chain.call('reader','GET','/api/v1/service-mappings',validate=lambda v:projection.check(v['rows'][0]['service']=='owned','scope'))
        row=emit.call_args.kwargs['interface'];normalized=actual_interface(row)
        self.assertEqual(row['actual']['status'],200);self.assertTrue(row['registration_evidence'])
        self.assertEqual(normalized['method'],'GET')
        self.assertEqual(normalized['path'],'/api/v1/service-mappings')

    def test_authorization_empty_read_never_proves_registration(self):
        value={'rows':[],'systems':[],'suggestions':0}
        with patch.object(projection,'transport',return_value=(200,json.dumps(value).encode(),{})),patch.object(projection,'emit'):
            self.chain.call('denied','GET','/api/v1/service-mappings',scenario='authorization',validate=lambda v:projection.check(not v['rows'],'leak'))
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text());self.assertFalse(row['registration_evidence'])

    def test_empty_memory_requires_honest_partial_dto_and_exact_unavailable_fields(self):
        value = dict(layers=[dict(id=k, count=0, evidence=dict(kind='current_published_facts', fact_ids=[]))
                             for k in ('service', 'workspace')], services=[], facts=[], conflicts=[],
                     service=None, conflict=None, policy=None, partial=True,
                     unavailable_fields=['working_layer', 'episodic_layer', 'semantic_layer',
                                         'confidence', 'recalls', 'expires', 'policy'])
        projection.verify_empty_memory(value)
        for field, bad in (('facts', [{'id':'fabricated'}]), ('partial', False),
                           ('policy', 'invented'), ('unavailable_fields', [])):
            with self.subTest(field=field), self.assertRaises(AssertionError):
                projection.verify_empty_memory(dict(value, **{field:bad}))
        broken=copy.deepcopy(value);broken['layers'][0]['evidence']['fact_ids']=['fabricated']
        with self.assertRaises(AssertionError):projection.verify_empty_memory(broken)

    def test_bulk_response_checks_item_error_and_exact_retry_effect(self):
        gid='group:'+'a'*24
        value=dict(action='claim', results=[dict(id=gid,outcome='claimed',matched=1,changed=1,
                    investigation_id=None,proposal_id=None,error=None)])
        projection.verify_bulk_claim(value,gid,'claimed',1)
        for field,bad in (('changed',0),('error','conflict'),('id','group:'+'b'*24),('matched',0)):
            changed=copy.deepcopy(value);changed['results'][0][field]=bad
            with self.subTest(field=field),self.assertRaises(AssertionError):
                projection.verify_bulk_claim(changed,gid,'claimed',1)
        value['results'][0].update(outcome='already_claimed',changed=0)
        projection.verify_bulk_claim(value,gid,'already_claimed',0)

    def test_added_targets_are_exact_and_memory_scope_is_explicit(self):
        value=projection.inventory(projection.ROOT)
        self.assertEqual(value['selected_route_count'],7)
        self.assertIn(('POST','/alert-groups/bulk'),projection.TARGETS)
        self.assertIn(('GET','/memory'),projection.TARGETS)
        self.assertIn('empty read',value['memory_authority_limit'])

    def test_bulk_protocol_readback_retry_and_denial_have_one_transactional_audit(self):
        gid='group:'+'a'*24;self.chain.users['reader']='reader-id'
        calls=[]
        def call(user,method,path,body=None,**kw):
            calls.append((user,method,path,body,kw))
            if method=='GET':
                value=dict(items=[dict(id=gid,source_id=self.chain.source_id,fingerprint=self.chain.source_id,
                           count=1,state='firing',owner=None if len(calls)==1 else self.chain.marker+' reader')])
            else:
                outcome='not_found' if user=='peer' else 'already_claimed' if kw.get('scenario')=='idempotency' else 'claimed'
                value=dict(action='claim',results=[dict(id=gid,outcome=outcome,matched=0 if user=='peer' else 1,
                           changed=1 if outcome=='claimed' else 0,investigation_id=None,proposal_id=None,
                           error='not_found' if user=='peer' else None)])
            if 'validate' in kw:kw['validate'](value)
            return value,{}
        with patch.object(self.chain,'call',side_effect=call),patch.object(self.chain,'sql',
                return_value=json.dumps(dict(members=1,audits=1,actor='reader-id'))) as sql,patch.object(projection,'emit'):
            self.chain.bulk_claim()
        requests=[c for c in calls if c[1]=='POST']
        self.assertEqual([c[4].get('scenario','normal') for c in requests],
                         ['normal','idempotency','authorization','boundary'])
        self.assertEqual(requests[2][4]['want'],207)
        self.assertEqual(calls[2][4]['index'],1)
        self.assertTrue(sql.call_args.kwargs['read'])
        self.assertTrue(sql.call_args.args[0].startswith('SELECT '))
        self.assertIn("action='alert.claim'",sql.call_args.args[0])

    def test_memory_protocol_uses_only_scoped_reads_preserves_audit_and_declares_limit(self):
        calls=[];proof=dict(facts=0,conflicts=0,audits=7)
        with patch.object(self.chain,'call',side_effect=lambda *a,**kw:calls.append((a,kw))), \
                patch.object(self.chain,'sql',return_value=json.dumps(proof)) as sql,patch.object(projection,'emit') as emit:
            self.chain.memory_empty_read()
        self.assertEqual(len(calls),4)
        self.assertEqual(calls[0][0],('reader','GET','/api/v1/memory'))
        self.assertIs(calls[0][1]['validate'],projection.verify_empty_memory)
        self.assertIsNone(calls[2][0][0]);self.assertEqual(calls[2][1]['want'],401)
        self.assertEqual(calls[3][1]['code'],'invalid_service')
        self.assertTrue(all(c.kwargs['read'] and c.args[0].startswith('SELECT ') for c in sql.call_args_list))
        self.assertIn('remain unproven',emit.call_args.args[2]['authority_limit'])
        with patch.object(self.chain,'call'),patch.object(self.chain,'sql',side_effect=[json.dumps(proof),json.dumps(dict(proof,audits=8))]),patch.object(projection,'emit'),self.assertRaises(AssertionError):
            self.chain.memory_empty_read()

    def test_memory_registered_success_explicitly_limits_empty_read_proof(self):
        value=dict(layers=[dict(id=k,name=k,count=0,description='source scope',
                     evidence=dict(kind='current_published_facts',fact_ids=[])) for k in ('service','workspace')],
                   services=[],facts=[],conflicts=[],service=None,conflict=None,policy=None,partial=True,
                   unavailable_fields=['working_layer','episodic_layer','semantic_layer','confidence','recalls','expires','policy'])
        with patch.object(projection,'transport',return_value=(200,json.dumps(value).encode(),{})),patch.object(projection,'emit'):
            self.chain.call('reader','GET','/api/v1/memory',validate=projection.verify_empty_memory)
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertTrue(row['registration_evidence'])
        self.assertEqual(row['actual']['status'],200)
        self.assertIn('no nonempty facts',row['proof_scope'])

    def test_first_failure_immutable_and_private(self):
        self.chain.first_red({'marker':'first'});self.chain.first_red({'marker':'later'})
        self.assertEqual(json.loads((self.chain.work/'first-failure.json').read_text()),{'marker':'first'})
        self.assertEqual(os.stat(self.chain.work/'first-failure.json').st_mode&0o777,0o600)

    def test_postmortem_protocol_uses_http_close_and_distinct_reviewer_not_sql_results(self):
        calls=[];self.chain.service_row={'application_id':'app'};self.chain.source_token='own-generated'
        def call(user,method,path,body=None,**kw):
            calls.append((user,method,path,body,kw))
            if path.startswith('/api/v1/webhooks/alertmanager/'):
                value={'accepted':1,'notification_delivery':'not_configured'}
            elif path=='/api/v1/incidents' and method=='GET':value={'items':[{'id':'alert'}],'counts':{'all':1}}
            elif path=='/api/v1/incidents' and method=='POST':value={'id':'case'}
            elif path=='/api/v1/incidents/case':value={'status':'resolved','status_revision':2,'resolved_status_event_id':42}
            elif path.startswith('/api/v1/postmortems/draft?'):value={'source_revision':'case:42'}
            elif path=='/api/v1/postmortems' and method=='POST':value={'id':'pm'}
            elif path.endswith('/publish'):value={'id':'pm','status':'published'}
            elif path.startswith('/api/v1/postmortems?tab='):
                value={'total':1,'page':1,'page_size':1,'items':[dict(id='pm',incident_id='case',status='published',
                    workflow_status='published',severity='P1',root_cause='Controlled local failure '+self.chain.nonce,
                    actions='unknown / 1',duration=None)],'partial':True,'complete_v3_dto':False,
                    'followups':None,'unknown_fields':['action_completion','items.duration','reported_mttr','reported_mttr_coverage'],
                    'kpis':[{'id':'postmortem_count','value':'1','series':None}]}
            else:value={'items':[],'total':0}
            if kw.get('validate') and kw.get('want',200)<300:kw['validate'](value)
            return value,{}
        proof=dict(close_event=42,source_event=42,publish_actor=self.chain.users['peer'],publish_count=1)
        with patch.object(self.chain,'call',side_effect=call),patch.object(self.chain,'sql',return_value=json.dumps(proof)) as sql, \
             patch.object(projection,'emit'):
            self.chain.postmortem()
        self.assertNotEqual(self.chain.users['reader'],self.chain.users['peer'])
        approvals=[x for x in calls if x[2].endswith('/publish')]
        self.assertEqual([x[0] for x in approvals],['reader','peer'])
        self.assertEqual(approvals[0][4]['want'],403)
        self.assertEqual(approvals[0][4]['code'],'self_review_forbidden')
        self.assertEqual([x for x in calls if x[2].endswith('/status')][0][3]['expected_revision'],1)
        self.assertEqual([x for x in calls if x[1]=='POST' and x[2]=='/api/v1/postmortems'][0][3]['source_revision'],'case:42')
        self.assertTrue(sql.call_args.kwargs['read'])
        self.assertNotIn('INSERT INTO',sql.call_args.args[0])

    def test_interruption_during_normal_cleanup_finishes_but_never_passes(self):
        handlers={};chain=Mock(work=self.chain.work);chain.run.return_value={'ok':True}
        def register(sig,callback):old=handlers.get(sig);handlers[sig]=callback;return old
        def cleanup():handlers[signal.SIGTERM](signal.SIGTERM,None);chain.cleaned=True
        chain.cleanup.side_effect=cleanup
        with patch.object(projection.signal,'signal',side_effect=register),patch.object(projection,'emit') as emit,self.assertRaises(projection.Blocked):projection.execute(chain)
        self.assertTrue(chain.cleaned);self.assertNotIn('projection.complete',[x.args[0] for x in emit.call_args_list])

    def test_primary_failure_and_cleanup_failure_both_retained(self):
        chain=Mock(work=self.chain.work);chain.run.side_effect=AssertionError('primary');chain.cleanup.side_effect=projection.Blocked('cleanup')
        with patch.object(projection,'emit') as emit,self.assertRaisesRegex(AssertionError,'primary'):projection.execute(chain)
        chain.cleanup.assert_called_once()
        names=[x.args[0] for x in emit.call_args_list]
        self.assertIn('projection.business',names);self.assertIn('projection.cleanup_failure',names);self.assertNotIn('projection.complete',names)

    def test_owned_setup_stop_uses_popen_handles_only_and_kills_after_timeout(self):
        proc=Mock(pid=123);proc.poll.side_effect=[None,0];proc.wait.side_effect=[subprocess.TimeoutExpired('owned',8),0]
        self.chain.processes=[proc]
        with patch.object(projection,'emit'),patch.object(projection.os,'kill') as kill:self.chain.stop_for_source_configuration()
        proc.terminate.assert_called_once();proc.kill.assert_called_once();kill.assert_not_called()

    def test_cleanup_without_allocated_db_does_not_query_shared_pg(self):
        with patch.object(self.chain,'pg') as pg,patch.object(projection,'emit'):
            self.chain.cleanup()
        pg.assert_not_called()
        self.assertFalse(json.loads((self.chain.work/'cleanup.json').read_text())['shared_database_touched'])

    def test_projection_validation_rejects_empty_wrong_ids_and_fabricated_analytics(self):
        pm={'total':1,'page':1,'page_size':1,'items':[dict(id='pm',incident_id='case',status='published',workflow_status='published',
            severity='P1',root_cause='root',actions='unknown / 1',duration=None)],'partial':True,'complete_v3_dto':False,
            'followups':None,'unknown_fields':['action_completion','items.duration','reported_mttr','reported_mttr_coverage'],
            'kpis':[{'id':'postmortem_count','value':'1','series':None}]}
        projection.verify_postmortem(pm,'pm','case','root')
        for changed in (dict(pm,items=[]),dict(pm,followups=[]),dict(pm,complete_v3_dto=True),dict(pm,kpis=[{'series':[0]}])):
            with self.assertRaises(AssertionError):projection.verify_postmortem(changed,'pm','case','root')
        with self.assertRaises(AssertionError):projection.verify_postmortem(pm,'other','case','root')

    def test_manual_case_summary_rejects_fabricated_duration_mttr_and_business_changes(self):
        pm={'total':1,'page':1,'page_size':1,'items':[dict(id='synthetic-pm',incident_id='synthetic-case',status='published',workflow_status='published',
            severity='P1',root_cause='synthetic root',actions='unknown / 1',duration=None)],'partial':True,'complete_v3_dto':False,
            'followups':None,'unknown_fields':['action_completion','items.duration','reported_mttr','reported_mttr_coverage'],
            'kpis':[{'id':'postmortem_count','value':'1','series':None}]}
        projection.verify_postmortem(pm,'synthetic-pm','synthetic-case','synthetic root')
        for field,wrong in (('duration','1m'),('duration',''),('root_cause','wrong'),('id','wrong'),('incident_id','wrong'),
                            ('status','draft'),('workflow_status','review'),('severity','P2'),('actions','unknown / 0')):
            with self.subTest(field=field,wrong=wrong):
                changed=copy.deepcopy(pm);changed['items'][0][field]=wrong
                with self.assertRaises(AssertionError):projection.verify_postmortem(changed,'synthetic-pm','synthetic-case','synthetic root')
        for missing in ('items.duration','reported_mttr','reported_mttr_coverage'):
            with self.subTest(missing=missing):
                changed=copy.deepcopy(pm);changed['unknown_fields'].remove(missing)
                with self.assertRaises(AssertionError):projection.verify_postmortem(changed,'synthetic-pm','synthetic-case','synthetic root')
        for mutation in ('mttr','count','empty'):
            with self.subTest(mutation=mutation):
                changed=copy.deepcopy(pm)
                if mutation=='mttr':changed['kpis'].append({'id':'reported_mttr','value':'0','series':None})
                elif mutation=='count':changed['total']=0
                else:changed['items']=[]
                with self.assertRaises(AssertionError):projection.verify_postmortem(changed,'synthetic-pm','synthetic-case','synthetic root')

    def test_push_views_require_non_synthetic_receipts_and_duplicate_truth(self):
        push=dict(types=[{'id':'change','today':'1'}],deliveries=[{'source':'s','code':'202','result':'Accepted'},
            {'source':'s','code':'409'}],availability='endpoint_host_unconfigured',
            endpoint={'host':'','method':'POST','path':'/api/v1/events/change'})
        projection.verify_push(push,'s')
        with self.assertRaises(AssertionError):projection.verify_push(dict(push,deliveries=[]),'s')
        bad=copy.deepcopy(push);bad['deliveries'][0]['result']='Test accepted'
        with self.assertRaises(AssertionError):projection.verify_push(bad,'s')
        webhook=dict(inbound=[dict(id='s:change',source_id='s',count='1',status_basis='latest_receipt_in_window',last_code=409,last_at='2026-10-11T00:00:00Z')],
            deliveries=[{'code':'202'},{'code':'409'}],availability={'api_tokens':'not_connected','outbound':'not_connected'})
        projection.verify_webhooks(webhook,'s')
        with self.assertRaises(AssertionError):projection.verify_webhooks(webhook,'hidden')

    def test_push_unconfigured_public_host_is_exact_not_general_availability(self):
        value=dict(types=[{'id':'change','today':'1'}],deliveries=[{'source':'synthetic-source','code':'202','result':'Accepted'},
            {'source':'synthetic-source','code':'409'}],availability='endpoint_host_unconfigured',
            endpoint={'host':'','method':'POST','path':'/api/v1/events/change'})
        projection.verify_push(value,'synthetic-source')
        for mutation in ('missing-host','nonempty-host','availability','method','path','accepted','duplicate','count','extra-receipt'):
            with self.subTest(mutation=mutation):
                changed=copy.deepcopy(value)
                if mutation=='missing-host':del changed['endpoint']['host']
                elif mutation=='nonempty-host':changed['endpoint']['host']='synthetic.example.invalid'
                elif mutation=='availability':changed['availability']='configured_unverified'
                elif mutation=='method':changed['endpoint']['method']='GET'
                elif mutation=='path':changed['endpoint']['path']='/api/v1/events/alert'
                elif mutation=='accepted':changed['deliveries']=changed['deliveries'][1:]
                elif mutation=='duplicate':changed['deliveries']=changed['deliveries'][:1]
                elif mutation=='count':changed['types'][0]['today']='0'
                else:changed['deliveries'].append(copy.deepcopy(changed['deliveries'][0]))
                with self.assertRaises(AssertionError):projection.verify_push(changed,'synthetic-source')

    def test_mapping_validator_requires_exact_resource_revision_and_committed_binding(self):
        binding=dict(system_id='metrics',kind='prometheus',connector_id='prometheus',resource='owned')
        value={'rows':[dict(service='owned',save=dict(bindings=[binding],binding_revision=2,editable=True),
            cells={'metrics':dict(state='ok',value='owned',brand='prometheus')})]}
        projection.verify_mapping(value,'owned',binding,2)
        for key,revision in (('hidden',2),('owned',1)):
            with self.assertRaises(AssertionError):projection.verify_mapping(value,key,binding,revision)
        with self.assertRaises(AssertionError):projection.verify_mapping({'rows':[]},'owned',binding,2)


if __name__=='__main__':unittest.main()

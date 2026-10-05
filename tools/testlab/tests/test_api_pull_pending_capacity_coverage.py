"""Offline contract/counterexamples only: no socket, Docker, PG or native binary."""
import collections, contextlib, copy, hashlib, importlib.util, io, json, pathlib, subprocess, sys
import signal, tempfile, threading, time, types, unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tools/testlab'))
import runner as strict_runner
spec=importlib.util.spec_from_file_location('offline_pull_capacity',ROOT/'tools/testlab/api_pull_pending_capacity_coverage.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

def query(expression='owned'):
    return '/api/v1/query_range?'+m.urlencode(dict(query=expression,start='2026-10-05T00:00:00Z',
        end='2026-10-05T00:05:00Z',step='30',timeout='5s',limit='100'))

def ledger(actors=('a','b','c','d'), pending=False):
    rows=[]; audits=[]
    for i,actor in enumerate(actors):
        rows.append(dict(id=i+1,source_id='source',actor_id=actor,kind='preview',
            status='pending' if pending else 'success',rows_count=1,dropped_count=0,error_code=''))
        if not pending:
            audits.append(dict(actor_id=actor,application_id='app',action='v3.ingest_pull.preview',target='source',outcome='success',
                changes=dict(source_id='source',environment_id='test',kind='preview',rows=1,dropped=0,error_code='')))
    return dict(attempts=rows,identities=[{'source_id':'source'}],audit=audits)

def dto_fixture():
    chain=types.SimpleNamespace(nonce='n'*32,app_key='owned-app',app='app',source_id='source',relay_url='http://127.0.0.1:18828',expression='owned')
    raw=json.dumps(dict(status='success',data=dict(resultType='matrix',result=[dict(
        metric=dict(signal_id=chain.nonce,application=chain.app_key,environment='test',service='pull-capacity'),values=[[1759708800,'7']])]))).encode()
    row=dict(service='app',name='pull-capacity',time='2025-10-06T00:00:00Z',value='7',labels={'signal_id':chain.nonce})
    # Derive fixture time with the same documented UTC representation, not a
    # hardcoded date mistaken for the epoch supplied above.
    row['time']=m.datetime.datetime.fromtimestamp(1759708800,m.datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    value=dict(id='source',contract='metric',contract_version='MetricSeries v1',step=3,
        request=dict(method='GET',host=chain.relay_url,path='/api/v1/query_range'),sample=dict(status=200,body=json.dumps([row])),
        preview=dict(rows=[row],checks=[{'label':s} for s in ['1 converted rows','0 dropped','Complete bounded response']]))
    return value,dict(status=200,raw=raw),chain

class CapacityTests(unittest.TestCase):
    def test_import_and_describe_do_not_acquire_resources(self):
        with patch.object(m.subprocess,'run',side_effect=AssertionError('Docker forbidden')),patch.object(m.socket,'socket',side_effect=AssertionError('socket forbidden')):
            description=m.describe()
        self.assertEqual(description['pull_requests'],6);self.assertEqual(description['normal_added'],0)
        self.assertEqual(description['cleanup_grace_seconds'],45);self.assertLess(description['hold_window_seconds'],description['upstream_deadline_seconds'])

    def test_registered_source_and_timeout_contract(self):
        source=(ROOT/'internal/v3ingestpull/service.go').read_text()
        protocol=(ROOT/'internal/v3integrations/prometheus_protocol.go').read_text()
        self.assertIn('sourceCount >= 60 || actorCount >= 10 || concurrent >= 4',source)
        self.assertIn("status='pending' AND started_at>clock_timestamp()-interval '15 seconds'",source)
        self.assertIn('context.WithTimeout(ctx, 5*time.Second)',protocol)
        self.assertIn('"timeout": {"5s"}',protocol)

    def test_formal_inventory_rejects_other_business_chain(self):
        contract=m.Contract(ROOT)
        self.assertEqual(contract.canonical('GET','/api/v1/ingest/pull/source')[0],'/api/v1/ingest/pull/{id}')
        for method,path in [('POST','/api/v1/ingest/pull/source/test'),('POST','/api/v1/incidents'),('GET','/api/v1/ingest/pull/source?x=1')]:
            with self.subTest(path=path),self.assertRaises(AssertionError):contract.canonical(method,path)

    def test_gate_four_real_receipts_are_held_and_bytes_preserved(self):
        calls=[]; results=[]; raw=b'{"status":"success","data":{"result":[]}}'
        def upstream(path):calls.append(path);return 200,raw,'application/json'
        gate=m.Gate('owned',upstream)
        threads=[threading.Thread(target=lambda:results.append(gate.read(query()))) for _ in range(4)]
        try:
            for thread in threads:thread.start()
            gate.wait_four();self.assertEqual(len(calls),4);self.assertEqual(results,[])
            self.assertEqual(len(gate.receipts),4)
            gate.release.set()
        finally:
            gate.release.set()
            for thread in threads:thread.join(2)
        self.assertEqual(results,[(200,raw,'application/json')]*4)
        self.assertTrue(all(r['sha256']==hashlib.sha256(raw).hexdigest() for r in gate.receipts.values()))

    def test_fifth_source_arrival_rejected_before_upstream(self):
        upstream=unittest.mock.Mock(return_value=(200,b'{}','application/json'))
        gate=m.Gate('owned',upstream);gate.first=time.monotonic();gate.arrivals=[{}]*4
        with self.assertRaises(AssertionError):gate.read(query())
        upstream.assert_not_called()

    def test_caller_launch_count_is_not_four_actual_arrivals(self):
        gate=m.Gate('owned',lambda p:None)
        gate.first=time.monotonic()-4;gate.arrivals=[{}]*3
        with self.assertRaises(m.Blocked):gate.wait_four()

    def test_four_arrivals_without_real_responses_cannot_pass_barrier(self):
        gate=m.Gate('owned',lambda p:None);gate.first=time.monotonic()-4;gate.arrivals=[{}]*4
        with self.assertRaises(m.Blocked):gate.wait_four()

    def test_relay_deadline_and_scope_fields_are_exact(self):
        gate=m.Gate('owned',lambda p:None)
        for path,auth in [(query().replace('5s','6s'),None),(query().replace('30','15'),None),(query()+'&unknown=1',None),
                          (query().replace('query=owned','query=other'),None),(query(),'Bearer forbidden'),('http://other'+query(),None)]:
            with self.subTest(path=path),self.assertRaises(AssertionError):gate.validate(path,auth)

    def test_expired_hold_is_marked_and_released_not_retried(self):
        calls=[];gate=m.Gate('owned',lambda p:(calls.append(p) or (200,b'{}','application/json')),clock=lambda:100)
        gate.first=90
        self.assertEqual(gate.read(query())[0],200)
        self.assertTrue(gate.expired);self.assertTrue(gate.release.is_set());self.assertEqual(len(calls),1)
        with self.assertRaises(m.Blocked):gate.wait_four()

    def test_pending_requires_exact_four_distinct_actors_and_no_audit(self):
        good=ledger(pending=True);m.pending_four(good,['a','b','c','d'],'source')
        for mutation in ['duplicate_actor','not_pending','wrong_source','audit','missing']:
            bad=copy.deepcopy(good)
            if mutation=='duplicate_actor':bad['attempts'][0]['actor_id']='b'
            elif mutation=='not_pending':bad['attempts'][0]['status']='success'
            elif mutation=='wrong_source':bad['attempts'][0]['source_id']='other'
            elif mutation=='audit':bad['audit']=[{}]
            else:bad['attempts'].pop()
            with self.subTest(mutation=mutation),self.assertRaises(AssertionError):m.pending_four(bad,['a','b','c','d'],'source')

    def test_completed_full_attempt_and_audit_contract(self):
        actors=['a','b','c','d','b'];good=ledger(actors);counts={'a':[1],'b':[1,1],'c':[1],'d':[1]}
        m.check_completed(good,actors,'source','app',counts)
        for mutation in ['pending','duplicate_id','wrong_actor','wrong_action','wrong_env','wrong_rows','audit_extra','attempt_extra']:
            bad=copy.deepcopy(good)
            if mutation=='pending':bad['attempts'][0]['status']='pending'
            elif mutation=='duplicate_id':bad['attempts'][0]['id']=bad['attempts'][1]['id']
            elif mutation=='wrong_actor':bad['audit'][0]['actor_id']='foreign'
            elif mutation=='wrong_action':bad['audit'][0]['action']='v3.other'
            elif mutation=='wrong_env':bad['audit'][0]['changes']['environment_id']='staging'
            elif mutation=='wrong_rows':bad['attempts'][0]['rows_count']=2
            elif mutation=='audit_extra':bad['audit'].append(copy.deepcopy(bad['audit'][0]))
            else:bad['attempts'].append(copy.deepcopy(bad['attempts'][0]))
            with self.subTest(mutation=mutation),self.assertRaises((AssertionError,KeyError)):
                m.check_completed(bad,actors,'source','app',counts)

    def test_dto_matches_original_timestamp_projection(self):
        value,receipt,chain=dto_fixture();self.assertEqual(m.validate_dto(value,receipt,chain),1)
        for mutation in ['time','app','label','value','count','raw_scope','raw_empty','raw_status']:
            v,r=copy.deepcopy(value),copy.deepcopy(receipt)
            if mutation=='time':v['preview']['rows'][0]['time']='wrong'
            elif mutation=='app':v['preview']['rows'][0]['service']='other'
            elif mutation=='label':v['preview']['rows'][0]['labels']['extra']='other'
            elif mutation=='value':v['preview']['rows'][0]['value']='8'
            elif mutation=='count':v['preview']['checks'][0]['label']='2 converted rows'
            else:
                raw=json.loads(r['raw'])
                if mutation=='raw_scope':raw['data']['result'][0]['metric']['signal_id']='other'
                elif mutation=='raw_empty':raw['data']['result']=[]
                else:raw['status']='error'
                r['raw']=json.dumps(raw).encode()
            with self.subTest(mutation=mutation),self.assertRaises(AssertionError):m.validate_dto(v,r,chain)

    def test_exact_absence_accepts_known_lowercase_only(self):
        for err in ['error: no such object: owned','Error: No such object: owned']:
            m.explicit_absence(subprocess.CompletedProcess([],1,'[]',err),'owned')
        for rc,out,err in [(1,'[]','Cannot connect to Docker daemon'),(1,'[]','permission denied'),(1,'[]','error: no such object: other'),
                           (125,'[]','error: no such object: owned'),(1,'[{"Id":"other"}]','error: no such object: owned'),(1,'[]','ERROR: NO SUCH OBJECT: owned')]:
            with self.subTest(err=err),self.assertRaises(m.Blocked):m.explicit_absence(subprocess.CompletedProcess([],rc,out,err),'owned')

    def test_exact_pg_identity_no_foreign_container(self):
        with patch.object(m,'_inspect_pg',return_value={'Id':'f'*64}),self.assertRaises(AssertionError):m.exact_pg('foreign')
        with patch.object(m,'_inspect_pg',return_value={'Id':m.PG_CID}):self.assertEqual(m.exact_pg('own')['Id'],m.PG_CID)

    def minimal_chain(self,folder):
        chain=object.__new__(m.Chain);chain.cleanup_deadline=None;chain.gate=None;chain.workers=[];chain.servers=[]
        chain.run_id='offline';chain.work=pathlib.Path(folder);chain.responses={};chain.users={};chain.prom_attempted=False;chain.prom_cid=None;chain.prom_name='owned'
        return chain

    def test_cleanup_releases_and_joins_even_if_readonly_cleanup_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.gate=types.SimpleNamespace(release=unittest.mock.Mock(),receipts={},condition=threading.Condition())
            worker=unittest.mock.Mock();worker.is_alive.return_value=False;chain.workers=[worker]
            observed=[]
            def cleanup(c):observed.append(c.cleanup_deadline);raise m.Blocked('DB cleanup fail')
            with patch.object(m,'_readonly_cleanup',side_effect=cleanup),patch.object(m.socket,'socket') as sock:
                sock.return_value.__enter__.return_value.connect_ex.return_value=1
                with self.assertRaises(m.Blocked):chain.cleanup()
            chain.gate.release.set.assert_called_once();worker.join.assert_called_once()
            self.assertEqual(observed,[chain.cleanup_deadline])

    def test_unknown_create_cid_never_adopted_or_deleted(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.prom_attempted=True
            with patch.object(m,'_readonly_cleanup'),patch.object(m,'inspect_container',return_value={'Id':'f'*64}),\
                 patch.object(m.subprocess,'run') as run,patch.object(m.socket,'socket') as sock:
                sock.return_value.__enter__.return_value.connect_ex.return_value=1
                with self.assertRaises(AssertionError):chain.cleanup()
                run.assert_not_called()

    def test_unknown_create_explicit_absence_closes_without_rm(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.prom_attempted=True
            with patch.object(m,'_readonly_cleanup'),patch.object(m,'inspect_container',return_value=None),\
                 patch.object(m.subprocess,'run') as run,patch.object(m.socket,'socket') as sock,patch.object(m,'emit'):
                sock.return_value.__enter__.return_value.connect_ex.return_value=1;chain.cleanup();run.assert_not_called()

    def test_daemon_error_does_not_close_unknown_create_cleanup(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.prom_attempted=True
            with patch.object(m,'_readonly_cleanup'),patch.object(m,'inspect_container',side_effect=m.Blocked('daemon unavailable')),\
                 patch.object(m.socket,'socket') as sock:
                sock.return_value.__enter__.return_value.connect_ex.return_value=1
                with self.assertRaises(m.Blocked):chain.cleanup()

    def test_single_cleanup_deadline_is_used_by_original_private_function(self):
        self.assertIn('deadline = self.cleanup_deadline',m._cleanup_source.replace(m._needle,'deadline = self.cleanup_deadline'))
        self.assertEqual(m._cleanup_source.count(m._needle),1)

    def test_formal_setup_grants_each_source_permission_without_business_sql(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.config_hashes={};chain.app_key='owned-app';chain.admin_email='own-bootstrap@testlab.invalid'
            chain.users=dict(reader='a',peer='b',hidden='c',denied='d')
            chain.roles={u:'role-'+u for u in chain.users};chain.source_id='source';chain.resource_id='resource'
            chain.relay_url='http://127.0.0.1:18828';chain.expression='owned'
            chain.call=unittest.mock.Mock(side_effect=[({'created':1},None),({'id':'connector'},None),
                (dict(id='target',connector_id='connector',environment_id='test',external_id='resource',target_kind='metric_resource'),None)])
            sql=[]
            def execute(statement,read=False):
                sql.append(statement)
                return ('fifth-admin' if 'FROM users' in statement else 'app') if read else ''
            chain.sql=execute;chain.stop_generation=unittest.mock.Mock();chain.start=unittest.mock.Mock()
            chain.setup_http()
            self.assertEqual([call.args[2] for call in chain.call.call_args_list],
                ['/api/v1/services','/api/v1/connectors','/api/v1/services/YXBwAHRlc3Q/external-targets'])
            mutations=[statement for statement in sql if statement.startswith('INSERT')]
            self.assertEqual(len(mutations),12)
            for role in chain.roles.values():
                for module,scope,scopeid in [('services','application','app'),('environment','environment','test'),('connectors','environment','test')]:
                    fragment=','.join(m.q(x) for x in [role,module,'view',scope,scopeid])
                    self.assertTrue(any(fragment in statement for statement in mutations),fragment)
            self.assertTrue(all(statement.startswith('INSERT INTO v3_role_grants(') for statement in mutations))
            chain.stop_generation.assert_called_once();chain.start.assert_called_once()

    def test_prom_cleanup_guard_requires_exact_owned_identity_and_resources(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.prom_cid='a'*64;chain.labels={'owner':'own'}
            chain.tmpfs='owned-tmpfs';chain.prom_args=['--owned']
            info=dict(Id=chain.prom_cid,Name='/owned',Image=m.PROM_IMAGE,Path='/bin/prometheus',Args=chain.prom_args,
                State={'Running':True},Config={'Labels':chain.labels,'User':str(m.os.getuid())+':'+str(m.os.getgid())},
                HostConfig=dict(NanoCpus=500000000,Memory=268435456,MemorySwap=268435456,Privileged=False,
                    ReadonlyRootfs=True,CapDrop=['ALL'],SecurityOpt=['no-new-privileges'],Tmpfs={'/prometheus':chain.tmpfs}),
                NetworkSettings={'Ports':{'9090/tcp':[dict(HostIp='127.0.0.1',HostPort=str(m.PROM_PORT))]}},
                Mounts=[dict(Type='bind',Source=str(chain.work/'prometheus.json'),Destination='/etc/prometheus/owned.json',RW=False)])
            self.assertEqual(m.guard_prom(info,chain),chain.prom_cid)
            for field in ['CID','image','owner','memory','port','mount']:
                bad=copy.deepcopy(info)
                if field=='CID':bad['Id']='f'*64
                elif field=='image':bad['Image']='other'
                elif field=='owner':bad['Config']['Labels']['owner']='foreign'
                elif field=='memory':bad['HostConfig']['Memory']=536870912
                elif field=='port':bad['NetworkSettings']['Ports']['9090/tcp'][0]['HostIp']='0.0.0.0'
                else:bad['Mounts'][0]['Source']='/foreign/config'
                with self.subTest(field=field),self.assertRaises(AssertionError):m.guard_prom(bad,chain)

    def test_unobservable_four_barrier_releases_workers_without_retry(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.frozen=unittest.mock.Mock()
            chain.users=dict(reader='a',peer='b',hidden='c',denied='d');chain.source_id='source'
            chain.ledger=unittest.mock.Mock(return_value={'attempts':[],'identities':[],'audit':[]})
            chain.request=unittest.mock.Mock()
            chain.gate=types.SimpleNamespace(release=threading.Event(),wait_four=unittest.mock.Mock(side_effect=m.Blocked('window')))
            try:
                with self.assertRaises(m.Blocked):chain.scenario()
            finally:
                for worker in chain.workers:worker.join(1)
            self.assertTrue(chain.gate.release.is_set());chain.gate.wait_four.assert_called_once()
            self.assertEqual(chain.request.call_count,4);chain.ledger.assert_called_once_with()

    def test_create_receipt_failure_retains_exact_cid_for_cleanup(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.config_hashes={};chain.app_key='owned'
            chain.nonce='n'*32;chain.expression='owned';chain.labels={'owner':'own'}
            chain.tmpfs='owned';chain.prom_args=['--owned'];cid='a'*64
            original=m.private_json
            def receipt(path,value):
                if path.name=='prom-create.json':raise OSError(28,'offline ENOSPC')
                return original(path,value)
            def command(argv,**kwargs):
                if argv[:2]==['docker','create']:return subprocess.CompletedProcess(argv,0,cid+'\n','')
                if argv==['docker','rm','-f',cid]:return subprocess.CompletedProcess(argv,0,cid+'\n','')
                raise AssertionError('unexpected native command in offline fixture')
            def exact_guard(info,owner,running=False):
                self.assertIs(owner,chain);self.assertEqual(owner.prom_cid,cid)
                self.assertEqual(info['Id'],cid);return cid
            with patch.object(m,'OwnedServer'),patch.object(m,'inspect_container',side_effect=[None,{'Id':cid},None]) as inspect,\
                 patch.object(m,'private_json',side_effect=receipt),patch.object(m.subprocess,'run',side_effect=command) as run,\
                 patch.object(m,'_readonly_cleanup'),patch.object(m,'guard_prom',side_effect=exact_guard) as guard,\
                 patch.object(m.socket,'socket') as sock,patch.object(m,'emit'):
                sock.return_value.__enter__.return_value.connect_ex.return_value=1
                with self.assertRaises(OSError):chain.start_source()
                chain.cleanup()
            self.assertEqual(chain.prom_cid,cid);guard.assert_called_once()
            self.assertEqual([call.args[0] for call in run.call_args_list][1:], [['docker','rm','-f',cid]])
            self.assertEqual([call.args[0] for call in inspect.call_args_list],['owned',cid,cid])

    def test_hashes_freeze_transitive_inventory_identity_and_own_tests(self):
        hashes=m.hashes()
        for name in ['identity.py','inventory.py','components.py','tests/test_api_pull_pending_capacity_coverage.py']:
            self.assertEqual(hashes[name],hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest())
        data=m.describe()
        self.assertEqual(data['helper_sha256'],hashes)

    def test_server_acquisition_cancellation_retains_handles_for_shutdown_join(self):
        for interrupted_constructor in [0,1]:
            with self.subTest(constructor=interrupted_constructor),tempfile.TemporaryDirectory() as folder:
                chain=self.minimal_chain(folder);chain.config_hashes={};chain.app_key='owned'
                chain.nonce='n'*32;chain.expression='owned';constructed=[];server_type=m.OwnedServer
                def constructor(port,handler):
                    server=object.__new__(server_type)
                    server.shutdown=unittest.mock.Mock();server.server_close=unittest.mock.Mock()
                    server.thread=unittest.mock.Mock();server.thread.is_alive.return_value=False
                    child=unittest.mock.Mock();child.is_alive.return_value=False
                    server.children={child};server.child_lock=threading.Lock()
                    constructed.append(server)
                    if len(constructed)-1==interrupted_constructor:signal.raise_signal(signal.SIGTERM)
                    return server
                with patch.object(m,'OwnedServer',side_effect=constructor),patch.object(m,'_readonly_cleanup'),\
                     patch.object(m.socket,'socket') as sock,patch.object(m,'emit'),m.ro.cooperative_signals():
                    sock.return_value.__enter__.return_value.connect_ex.return_value=1
                    with self.assertRaises(m.Blocked):chain.start_source()
                    chain.cleanup()
                self.assertEqual(chain.servers,constructed)
                self.assertEqual(len(chain.servers),interrupted_constructor+1)
                for server in constructed:
                    server.shutdown.assert_called_once();server.server_close.assert_called_once()
                    server.thread.join.assert_called_once()
                    next(iter(server.children)).join.assert_called_once()

    def test_fifth_request_uses_authorized_identity_with_zero_pending(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.frozen=unittest.mock.Mock()
            chain.users=dict(reader='a',peer='b',hidden='c',denied='d');chain.admin_actor='fifth-admin';chain.source_id='source'
            chain.ledger=unittest.mock.Mock(side_effect=[{'attempts':[],'identities':[],'audit':[]},ledger(pending=True)])
            def response(key,user,index,timeout=7):
                chain.responses[key]={'status':418,'user':user,'index':index}
            chain.request=unittest.mock.Mock(side_effect=response)
            chain.gate=types.SimpleNamespace(release=threading.Event(),wait_four=unittest.mock.Mock(return_value=time.monotonic()+3))
            try:
                with self.assertRaises(AssertionError):chain.scenario()  # Stop immediately after fifth receipt.
            finally:
                for worker in chain.workers:worker.join(1)
            fifth=[call for call in chain.request.call_args_list if call.args[0]=='fifth']
            self.assertEqual(len(fifth),1);self.assertEqual(fifth[0].args[1],'admin')
            self.assertNotIn(chain.admin_actor,[row['actor_id'] for row in ledger(pending=True)['attempts']])
            self.assertTrue(chain.gate.release.is_set())

    def full_prom_info(self,chain):
        return dict(Id=chain.prom_cid,Name='/'+chain.prom_name,Image=m.PROM_IMAGE,Path='/bin/prometheus',Args=chain.prom_args,
            State={'Running':True},Config={'Labels':chain.labels,'User':str(m.os.getuid())+':'+str(m.os.getgid())},
            HostConfig=dict(NanoCpus=500000000,Memory=268435456,MemorySwap=268435456,Privileged=False,
                ReadonlyRootfs=True,CapDrop=['ALL'],SecurityOpt=['no-new-privileges'],Tmpfs={'/prometheus':chain.tmpfs}),
            NetworkSettings={'Ports':{'9090/tcp':[dict(HostIp='127.0.0.1',HostPort=str(m.PROM_PORT))]}},
            Mounts=[dict(Type='bind',Source=str(chain.work/'prometheus.json'),Destination='/etc/prometheus/owned.json',RW=False)])

    def test_persistent_cleanup_receipt_enospc_still_guards_removes_verifies_absence_and_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.prom_attempted=True;chain.prom_cid='a'*64
            chain.labels={'owner':'own'};chain.tmpfs='owned';chain.prom_args=['--owned']
            cid=chain.prom_cid
            results=[subprocess.CompletedProcess([],0,json.dumps([self.full_prom_info(chain)]),''),
                subprocess.CompletedProcess([],0,cid+'\n',''),
                subprocess.CompletedProcess([],1,'[]','error: no such object: '+cid)]
            with patch.object(m,'private_json',side_effect=OSError(28,'persistent offline ENOSPC')) as receipt,\
                 patch.object(m.subprocess,'run',side_effect=results) as run,patch.object(m,'_readonly_cleanup'),\
                 patch.object(m,'guard_prom',wraps=m.guard_prom) as guard,patch.object(m.socket,'socket') as sock,patch.object(m,'emit') as emit:
                sock.return_value.__enter__.return_value.connect_ex.return_value=1
                with self.assertRaises(OSError) as failure:chain.cleanup()
            self.assertEqual(failure.exception.errno,28)
            self.assertEqual([call.args[0] for call in run.call_args_list],
                [['docker','inspect',cid],['docker','rm','-f',cid],['docker','inspect',cid]])
            self.assertEqual([call.args[0].name for call in receipt.call_args_list],
                ['container-inspect-cleanup-before.json','prom-remove.json','container-inspect-cleanup-after.json'])
            guard.assert_called_once();emit.assert_not_called()

    def test_cleanup_receipt_failure_never_authorizes_unsafe_deletion(self):
        for mutation in ['malformed','ambiguous','foreign','wrong_owner','unknown_cid']:
            with self.subTest(mutation=mutation),tempfile.TemporaryDirectory() as folder:
                chain=self.minimal_chain(folder);chain.prom_attempted=True;chain.prom_cid='a'*64
                chain.labels={'owner':'own'};chain.tmpfs='owned';chain.prom_args=['--owned']
                info=self.full_prom_info(chain);raw=json.dumps([info])
                if mutation=='malformed':raw='bad-json'
                elif mutation=='ambiguous':raw=json.dumps([info,info])
                elif mutation=='foreign':info['Id']='f'*64;raw=json.dumps([info])
                elif mutation=='wrong_owner':info['Config']['Labels']={'owner':'foreign'};raw=json.dumps([info])
                else:chain.prom_cid=None
                with patch.object(m,'private_json',side_effect=OSError(28,'persistent offline ENOSPC')),\
                     patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0,raw,'')) as run,\
                     patch.object(m,'_readonly_cleanup'),patch.object(m.socket,'socket') as sock:
                    sock.return_value.__enter__.return_value.connect_ex.return_value=1
                    with self.assertRaises(OSError):chain.cleanup()
                self.assertEqual(run.call_count,1)
                self.assertEqual(run.call_args.args[0][:2],['docker','inspect'])

    def test_noncleanup_inspection_receipt_failure_remains_failclosed(self):
        with tempfile.TemporaryDirectory() as folder,patch.object(m,'private_json',side_effect=OSError(28,'offline ENOSPC')),\
             patch.object(m.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'[{"Id":"held"}]','')):
            with self.assertRaises(OSError):m.inspect_container('owned',pathlib.Path(folder),'created')

    def strict_parse_rows(self,rows):
        # Exercise the unchanged actual Runner parser, without its Store
        # constructor, subprocess or any filesystem ledger writes.
        runner=object.__new__(strict_runner.Runner);runner.root=ROOT;runner.run_id='offline'
        runner.prepared={};runner.stop=threading.Event();runner.store=unittest.mock.Mock()
        runner.store.add_case.side_effect=lambda *args,**kwargs:dict(id=kwargs['name'],**kwargs)
        runner.store.start_attempt.side_effect=lambda case:dict(id=case['id']+'-attempt',status='running')
        runner.store.write_evidence.return_value='offline-evidence'
        outputs=[('stdout',json.dumps(row)+'\n') for row in rows]
        process=unittest.mock.Mock();process.wait.return_value=0
        with patch.object(strict_runner,'command_env',return_value={}),\
             patch.object(strict_runner.subprocess,'Popen',return_value=process),\
             patch.object(strict_runner,'command_output',return_value=outputs):
            runner.execute_command({'id':m.MODULE},{'id':'offline-suite'},
                {'argv':['offline-no-exec'],'parser':'testlab_json','timeout_seconds':300,'cleanup_grace_seconds':45})
        return runner.store.finish_attempt.call_args_list

    def test_two_generations_keep_ready_results_unique_for_actual_strict_runner(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.generation=0;chain.configured=False;chain.state={}
            for name in ['instance.json','models.json','commands.json']:(chain.work/name).write_text('{}')
            def readiness(instance):
                for index in range(2):m.ro.emit('readonly.api_ready.'+str(index),'passed',{'port':m.PORTS[index],'pid':100+instance.generation*2+index})
            output=io.StringIO()
            original_emit=m.ro.emit
            with patch.object(m.ro.Chain,'start',autospec=True,side_effect=readiness),contextlib.redirect_stdout(output):
                chain.start();chain.start()
            self.assertIs(m.ro.emit,original_emit)
        rows=[json.loads(line) for line in output.getvalue().splitlines()]
        finished=self.strict_parse_rows(rows)
        self.assertTrue(all(call.args[2]=='passed' for call in finished),[call.args[3] for call in finished])
        self.assertEqual(len(finished),4)
        self.assertEqual({row['name'] for row in rows},{'pull_capacity.generation.'+str(g)+'.api_ready.'+str(i) for g in range(2) for i in range(2)})
        self.assertEqual([row['actual']['port'] for row in rows],list(m.PORTS)*2)

    def test_actual_strict_runner_still_rejects_immutable_duplicate_ready(self):
        row=dict(name='readonly.api_ready.0',module=m.MODULE,status='passed',actual={'port':m.PORTS[0]})
        finished=self.strict_parse_rows([row,row])
        audits=[call for call in finished if call.args[2]=='failed']
        self.assertEqual(len(audits),1);self.assertIn('malformed=True',audits[0].args[3])

    def test_capture_io_redacts_cookies_and_pins_exact_bytes(self):
        with tempfile.TemporaryDirectory() as folder:
            chain=self.minimal_chain(folder);chain.users={'reader':'actor'};chain.admin_actor='own-admin'
            chain.responses={'fifth':dict(status=429,user='admin',index=1,raw=b'{"error":{"code":"rate_limited"}}',headers={'Cookie':'forbidden'})}
            chain.capture_io();path=pathlib.Path(folder)/'received-fifth.json';value=json.loads(path.read_text())
            self.assertEqual(value['actor_id'],'own-admin');self.assertNotIn('forbidden',path.read_text());self.assertEqual(value['sha256'],hashlib.sha256(chain.responses['fifth']['raw']).hexdigest())
            self.assertEqual(path.stat().st_mode&0o777,0o600)

if __name__=='__main__':unittest.main()

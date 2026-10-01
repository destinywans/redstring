"""Offline acceptance of guard boundaries and independent exact sample oracle."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rules_read_functional_coverage as rules

class RuleReadTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        for f in ('docs/api/v3-contract.md','internal/apicontract/openapi.json'):
            p=self.root/f;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes((rules.ROOT/f).read_bytes())
        data={'owner':rules.OWNER,'binary_sha256':'offline','source_tree_sha':'offline','helper_sha256':rules.hashes(),'postgres_container':'owned'}
        self.chain=rules.Chain(self.root,data,str(uuid.uuid4()));self.chain.app='a'*32;self.chain.expr=rules.rule_config(self.chain.app,self.chain.nonce)['groups'][0]['rules'][0]['expr']
        self.info={'Name':'/'+self.chain.prom_name,'Id':'c'*64,'Image':rules.PROM_IMAGE_ID,'State':{'Running':True},
          'Config':{'Labels':{'aiops.testlab.owner':rules.OWNER,'aiops.testlab.run':self.chain.run_id,'aiops.testlab.purpose':'rule-read'}},
          'HostConfig':{'NanoCpus':1000000000,'Memory':536870912,'Privileged':False,'ReadonlyRootfs':True,'PortBindings':{'9090/tcp':[{'HostIp':'127.0.0.1','HostPort':str(rules.PROM_PORT)}]}},
          'NetworkSettings':{'Ports':{'9090/tcp':[{'HostIp':'127.0.0.1','HostPort':str(rules.PROM_PORT)}]}},
          'Mounts':[{'Type':'bind','Source':str(self.chain.work/n),'Destination':dst,'RW':False} for n,dst in [('prometheus.json','/etc/prometheus/prometheus.yml'),('rules.json',rules.FILE)]]}
    def source(self):
        source=rules.Source.__new__(rules.Source);source.chain=self.chain;return source
    def matrix(self):
        return {'path':'/api/v1/query_range?'+rules.urlencode({'query':self.chain.expr,'start':'1790758397.123456789','end':'1790780012.123456789','step':'15','limit':'101','timeout':'15s'}),
          'response':{'status':'success','data':{'resultType':'matrix','result':[{'metric':{'application':self.chain.app,'environment':'test','fixture':self.chain.nonce},'values':[[1790780000.123,'2'],[1790780015.123,'3']]}]}}}
    def observed(self):
        r=rules.rule_config(self.chain.app,self.chain.nonce)['groups'][0]['rules'][0]
        return {'status':'success','data':{'groups':[{'file':rules.FILE,'name':rules.GROUP,'interval':15,'rules':[{'name':r['alert'],'type':'alerting','query':r['expr'],'duration':0,'labels':r['labels'],'annotations':r['annotations'],'state':'firing','health':'ok','lastEvaluation':'2026-10-01T12:00:00Z'}]}]}}
    def test_inventory_is_offline_and_five_nonoverlapping_ports(self):
        with patch.object(rules,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(rules.main(['--inventory']),0)
        d=json.loads(out.getvalue());self.assertEqual(d['ports'],[18628,18629,18630,18631,18635]);self.assertEqual(len(d['targets']),3);self.assertTrue(d['offline_only']);self.assertFalse(d['full_history_verified']);doctor.assert_not_called()
    def test_actual_execution_requires_live(self):
        with contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):rules.main([])
    def test_native_scope_and_no_notification_config(self):
        cfg=rules.prometheus_config();self.assertNotIn('alerting',cfg);self.assertEqual(cfg['rule_files'],[rules.FILE]);self.assertEqual(cfg['scrape_configs'][0]['static_configs'][0]['targets'],['host.docker.internal:18631'])
        r=rules.rule_config(self.chain.app,self.chain.nonce)['groups'][0];self.assertEqual(r['interval'],'15s');self.assertEqual(r['rules'][0]['labels']['application'],self.chain.app)
        with self.assertRaises(rules.Blocked):rules.rule_config('testlab',self.chain.nonce)
    def test_contract_bounds_three_main_two_auxiliary(self):
        for method,path in rules.TARGETS:self.assertEqual(self.chain.contract.canonical(method,'/api/v1'+path.replace('{id}','owned'))[1],'main')
        self.assertEqual(self.chain.contract.canonical('GET','/api/v1/alert-rules/observed')[1],'auxiliary_or_historical')
        for method,path in [('POST','/api/v1/alert-rules'),('PUT','/api/v1/alert-rules/id'),('POST','/api/v1/alert-rules/generate'),('GET','https://example.invalid/api/v1/alert-rules')]:
            with self.assertRaises(rules.Blocked):self.chain.contract.canonical(method,path)
    def test_port_context_restores_parent_even_on_exception(self):
        original=rules.readonly.PORTS,rules.readonly.BASES
        with self.assertRaises(RuntimeError),rules.api_ports():
            self.assertEqual(rules.readonly.PORTS,rules.PORTS);raise RuntimeError('controlled')
        self.assertEqual((rules.readonly.PORTS,rules.readonly.BASES),original)
    def test_owned_identity_accepts_running_and_stopped_same_cid(self):
        self.assertEqual(rules.guard_owned_prom(self.info,self.chain),'c'*64)
        self.chain.prom_cid='c'*64;info=copy.deepcopy(self.info);info['State']['Running']=False;info['NetworkSettings']['Ports']={}
        self.assertEqual(rules.guard_owned_prom(info,self.chain,expect_running=False),'c'*64)
        with self.assertRaises(rules.Blocked):rules.guard_owned_prom(info,self.chain)
    def test_identity_rejects_owner_run_cid_image_and_resource_drift(self):
        for mutate in [lambda d:d.update(Name='/other'),lambda d:d.update(Image='sha256:'+'d'*64),lambda d:d['Config']['Labels'].update({'aiops.testlab.run':str(uuid.uuid4())}),lambda d:d['HostConfig'].update(Memory=1073741824),lambda d:d['HostConfig'].update(Privileged=True),lambda d:d['HostConfig'].update(ReadonlyRootfs=False),lambda d:d['NetworkSettings']['Ports']['9090/tcp'][0].update(HostIp='0.0.0.0'),lambda d:d['Mounts'][0].update(RW=True),lambda d:d['Mounts'][0].update(Source='/shared/config')]:
            d=copy.deepcopy(self.info);mutate(d)
            with self.assertRaises(rules.Blocked):rules.guard_owned_prom(d,self.chain)
        self.chain.prom_cid='f'*64
        with self.assertRaises(rules.Blocked):rules.guard_owned_prom(self.info,self.chain)
    def test_normal_target_needs_business_validator_before_transport(self):
        with patch.object(rules.readonly.Chain,'call') as call:
            with self.assertRaises(rules.Blocked):self.chain.call('reader','GET','/api/v1/alert-rules')
            call.assert_not_called()
    def test_pg_exact_cid_checked_before_all_inherited_sql(self):
        with patch.object(rules.readonly,'inspect_pg',return_value={'Id':'foreign'}),patch.object(rules.readonly.Chain,'pg') as pg,self.assertRaises(rules.Blocked):self.chain.pg('DROP DATABASE own')
        pg.assert_not_called()
    def test_exhausted_aggregate_cleanup_budget_prevents_pg_access(self):
        self.chain.cleanup_deadline=0
        with patch.object(rules.readonly,'inspect_pg') as inspect,patch.object(rules.readonly.Chain,'pg') as pg,self.assertRaises(rules.Blocked):self.chain.pg('DROP DATABASE own')
        inspect.assert_not_called();pg.assert_not_called()
    def test_aggregate_cleanup_deadline_clamps_parent_cleanup_budget(self):
        self.chain.cleanup_deadline=rules.time.monotonic()+10
        with patch.object(rules.readonly,'inspect_pg',return_value={'Id':rules.PG_CID}),patch.object(rules.readonly.Chain,'pg',return_value='ok') as pg:
            self.chain.pg('SELECT 1',deadline=rules.time.monotonic()+40)
        self.assertEqual(pg.call_args.kwargs['deadline'],self.chain.cleanup_deadline)
    def test_helper_change_before_parent_or_container_access(self):
        with patch.object(rules,'hashes',return_value={}),patch.object(rules.readonly.Chain,'frozen') as frozen,patch.object(self.chain,'guard_prom') as guard,self.assertRaises(rules.Blocked):self.chain.frozen()
        frozen.assert_not_called();guard.assert_not_called()
    def test_relay_exact_rules_and_range_only(self):
        source=self.source();self.assertEqual(source.validate('/api/v1/rules?type=alert'),'/api/v1/rules?type=alert')
        self.assertEqual(source.validate(self.matrix()['path']),self.matrix()['path'])
        for path in ['/api/v1/rules?type=record','/api/v1/rules?type=alert&type=alert','http://external/api/v1/rules?type=alert','/api/v1/admin/tsdb/delete_series','/-/reload',self.matrix()['path'].replace('step=15','step=2'),self.matrix()['path'].replace('limit=101','limit=100'),self.matrix()['path'].replace('testlab_rules_fixture_total','other')]:
            with self.assertRaises(rules.Blocked):source.validate(path)
    def test_direct_transport_rejects_write_or_external_without_connection(self):
        for path in ['http://external/api/v1/rules','/-/reload','/api/v1/admin/tsdb/delete_series']:
            with patch.object(rules.http.client,'HTTPConnection') as conn,self.assertRaises(rules.Blocked):rules.direct_get(path)
            conn.assert_not_called()
    def test_actual_loaded_rule_exact_config_and_real_evaluation_required(self):
        self.assertEqual(rules.validate_observed(self.observed(),self.chain.app,self.chain.nonce)['state'],'firing')
        for field,value in [('interval',2),('file','/shared/rules'),('name','other')]:
            data=self.observed();data['data']['groups'][0][field]=value
            with self.assertRaises(AssertionError):rules.validate_observed(data,self.chain.app,self.chain.nonce)
        for field,value in [('labels',{'severity':'P3'}),('query','vector(1)'),('state','inactive'),('health','err')]:
            data=self.observed();data['data']['groups'][0]['rules'][0][field]=value
            with self.assertRaises(AssertionError):rules.validate_observed(data,self.chain.app,self.chain.nonce)
    def test_original_shared_rule_lacks_labels_and_interval_cannot_pass(self):
        d=self.observed();d['data']['groups'][0]['interval']=2;d['data']['groups'][0]['rules'][0]['labels']={'severity':'P3'}
        with self.assertRaises(AssertionError):rules.validate_observed(d,self.chain.app,self.chain.nonce)
    def test_exact_windows_from_natural_samples_keep_precision(self):
        row=self.matrix();row['response']['data']['result'][0]['values']=[[1790779997.123,'2'],[1790780012.123,'3']]
        windows,proof=rules.current_windows(row,self.chain.app,self.chain.nonce)
        self.assertEqual(windows,['2026-09-30T14:53:17.123456789Z/2026-09-30T14:53:32.123456789Z']);self.assertFalse(proof['six_hours_history_proven']);self.assertFalse(proof['continuous_trial_proven']);self.assertEqual(proof['actual_samples'],2)
    def test_range_partial_scope_empty_nonfinite_offgrid_and_duplicate_rejected(self):
        valid=self.matrix();valid['response']['data']['result'][0]['values']=[[1790779997.123,'2'],[1790780012.123,'3']]
        for mutate in [lambda d:d['response'].update(warnings=['partial']),lambda d:d['response']['data'].update(result=[]),lambda d:d['response']['data']['result'][0]['metric'].update(application='foreign'),lambda d:d['response']['data']['result'][0]['values'][0].__setitem__(1,'NaN'),lambda d:d['response']['data']['result'][0]['values'][0].__setitem__(0,1790779998.123),lambda d:d['response']['data']['result'][0].update(values=[[1790780012.123,'2'],[1790780012.123,'3']])]:
            row=copy.deepcopy(valid);mutate(row)
            with self.assertRaises(AssertionError):rules.current_windows(row,self.chain.app,self.chain.nonce)
    def test_cleanup_attempts_source_and_pg_even_if_container_guard_fails(self):
        self.chain.prom_attempted=True;self.chain.source=Mock()
        with patch.object(rules.readonly,'inspect_pg',return_value={'foreign':True}),patch.object(rules.readonly.Chain,'cleanup') as cleanup,self.assertRaises(rules.Blocked):self.chain.cleanup()
        self.chain.source.close.assert_called_once();cleanup.assert_called_once()
    def test_second_generation_preserves_first_logs_before_exclusive_start(self):
        self.chain.processes=[Mock(pid=11),Mock(pid=12)]
        for held in self.chain.processes:held.poll.side_effect=[None,0]
        for i in (0,1):
            p=self.chain.work/('api-'+str(i)+'.log');p.write_text('first generation '+str(i));p.chmod(0o600)
        def start():
            for i in (0,1):
                p=self.chain.work/('api-'+str(i)+'.log')
                with p.open('x') as f:f.write('second generation '+str(i))
        with patch.object(self.chain,'start',side_effect=start):self.chain.restart_with_source()
        for i,held in enumerate(self.chain.processes):
            held.terminate.assert_called_once();held.wait.assert_called_once_with(timeout=10)
            self.assertEqual((self.chain.work/('api-bootstrap-'+str(i)+'.log')).read_text(),'first generation '+str(i))
            self.assertEqual((self.chain.work/('api-'+str(i)+'.log')).read_text(),'second generation '+str(i))
        self.assertTrue((self.chain.work/'bootstrap-generation-exits.json').exists())
    def test_cleanup_marker_does_not_reinspect_already_removed_source(self):
        import inspect
        source=inspect.getsource(rules.Chain.cleanup)
        self.assertNotIn("self.case_marker('rules.cleanup'",source)
        self.assertIn("['docker','container','inspect',self.prom_name]",source)
        self.assertIn("'exact_name_absent':True",source)
    def test_source_sql_cannot_create_rules_audit_or_success_ledgers(self):
        import inspect
        source=inspect.getsource(rules.Chain.prepare_source)
        self.assertIn("'POST','/api/v1/services'",source);self.assertIn('SELECT id FROM applications',source)
        self.assertNotIn('INSERT INTO',source);self.assertNotIn('UPDATE ',source);self.assertNotIn('management_audit',source)
    def test_two_api_generations_emit_distinct_immutable_attempt_keys(self):
        original=rules.readonly.emit
        def base_start(_):
            for index in (0,1):
                rules.readonly.emit('readonly.api_ready.'+str(index),'passed',{'port':rules.PORTS[index]})
                self.chain.processes.append(Mock())
        with patch.object(rules.readonly.Chain,'start',base_start),contextlib.redirect_stdout(io.StringIO()) as output:
            self.chain.start();self.chain.start()
        rows=[json.loads(line) for line in output.getvalue().splitlines()]
        keys=[(row['module'],row['name']) for row in rows]
        self.assertEqual(len(keys),4);self.assertEqual(len(set(keys)),4)
        self.assertEqual([row['name'] for row in rows],['rules.api_ready.generation.1.instance.0','rules.api_ready.generation.1.instance.1','rules.api_ready.generation.2.instance.0','rules.api_ready.generation.2.instance.1'])
        self.assertIs(rules.readonly.emit,original)
    def test_start_restores_scoped_emitter_after_failed_bootstrap(self):
        original=rules.readonly.emit
        with patch.object(rules.readonly.Chain,'start',side_effect=rules.Blocked('controlled')):
            with self.assertRaises(rules.Blocked):self.chain.start()
        self.assertIs(rules.readonly.emit,original)

if __name__=='__main__':unittest.main()

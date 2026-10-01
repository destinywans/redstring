import contextlib,copy,io,json,sys,tempfile,threading,unittest
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_config_drafts_functional_coverage as drafts
class DraftsTests(unittest.TestCase):
    def setUp(self):
        self.app='a'*32;self.nonce='b'*32;self.pid=42;self.spec=drafts.draft_spec(self.app,'test',self.nonce,'metric > 0','owned')
        self.mr={'project_id':'42','iid':1,'url':'https://localhost:18580/owned/project/-/merge_requests/1'}
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
    def test_machine_contract_all_targets_and_activation_rejected(self):
        c=drafts.Contract(drafts.ROOT)
        for m,p in drafts.TARGETS:self.assertEqual(c.canonical(m,'/api/v1'+p.replace('{id}','c'*32))[0],'/api/v1'+p)
        with self.assertRaises(drafts.Blocked):c.canonical('POST','/api/v1/alert-rules/'+'c'*32+'/enable')
    def test_post_config_drafts_contract_accepted_not_created(self):
        c=drafts.Contract(drafts.ROOT)
        for p in ('/inhibitions','/notification-routes'):
            responses=c.api['paths'][p]['post']['responses'];self.assertIn('202',responses)
    def test_import_json_yaml_uses_native_scope_no_interpolation(self):
        body=drafts.import_input(self.spec);rule=json.loads(body['yaml'])['groups'][0]['rules'][0];self.assertEqual(rule['labels']['application'],self.app);self.assertEqual(body['mode'],'draft')
    def test_namespace_three_disjoint_target_and_prefixes(self):
        b=drafts.branches(self.nonce);self.assertEqual(len({x['target'] for x in b.values()}),3);self.assertEqual(b['rules']['prefix'],'aiops-alert-rule-drafts/')
        for a in b.values():
            for other in b.values():
                if a is not other:self.assertFalse(a['prefix'].startswith(other['prefix']))
    def test_native_import_reference_and_version_required(self):
        v={'status':'pending_review','candidates':[{'name':self.spec['name'],'group':'owned','draft_id':'c'*32,'outcome':'new','version':1}],'mr':self.mr}
        self.assertEqual(drafts.imported_draft(v,self.spec,42),('c'*32,1))
        for field,val in [('outcome','duplicate'),('version',2),('draft_id','fake')]:
            bad=copy.deepcopy(v);bad['candidates'][0][field]=val
            with self.assertRaises(AssertionError):drafts.imported_draft(bad,self.spec,42)
    def test_mr_wrong_project_host_port_iid_credentials_rejected(self):
        self.assertEqual(drafts.mr_identity(self.mr,42),1)
        for patcher in [dict(project_id='43'),dict(iid=True),dict(url='https://foreign.example/owned/-/merge_requests/1'),dict(url='https://localhost:18581/owned/-/merge_requests/1'),dict(url='https://user:secret@localhost:18580/owned/-/merge_requests/1'),dict(url='https://localhost:18580/owned/-/merge_requests/2')]:
            with self.assertRaises(AssertionError):drafts.mr_identity(dict(self.mr,**patcher),42)
    def test_open_unmerged_exact_branches_required(self):
        v=dict(project_id=42,source_project_id=42,target_project_id=42,iid=1,state='opened',merged_at=None,target_branch='rules-'+self.nonce[:12],source_branch='aiops-alert-rule-drafts/x')
        drafts.verify_mr(v,42,1,v['target_branch'],'aiops-alert-rule-drafts/')
        for p in [dict(state='merged'),dict(merged_at='now'),dict(source_project_id=43),dict(source_branch='main'),dict(target_branch='main')]:
            with self.assertRaises(AssertionError):drafts.verify_mr(dict(v,**p),42,1,v['target_branch'],'aiops-alert-rule-drafts/')
    def test_etag_quoted_exact_not_silently_normalized(self):
        value='"sha256:'+'a'*64+'"';self.assertEqual(drafts.etag(value),value)
        for bad in (value[1:-1],value+' ', '"sha256:'+'a'*63+'"'):
            with self.assertRaises(AssertionError):drafts.etag(bad)
    def test_cas_version_cannot_be_float_or_unchanged(self):
        v={'draft_id':'c'*32,'status':'pending_review','version':2,'mr':self.mr};self.assertEqual(drafts.updated_draft(v,'c'*32,1,42),1)
        for version in (1,2.0,True):
            with self.assertRaises(AssertionError):drafts.updated_draft(dict(v,version=version),'c'*32,1,42)
    def test_preview_synthetic_history_mode_and_exact_seven_days(self):
        v=dict(environment_id='test',application_id=self.app,config_revision='sha256:'+'a'*64,basis='complete_history_simulation',history_complete=True,potentially_suppressed=1,sample=[{}],actual_inhibition_hits=None,window_start='2026-09-24T00:00:00Z',window_end='2026-10-01T00:00:00Z')
        revision='"sha256:'+'a'*64+'"';drafts.preview(v,self.app,revision,1,'protocol_mock')
        for p in [dict(window_start='2026-09-30T00:00:00Z'),dict(actual_inhibition_hits=1),dict(potentially_suppressed=True),dict(history_complete=False)]:
            with self.assertRaises(AssertionError):drafts.preview(dict(v,**p),self.app,revision,1,'protocol_mock')
    def test_live_missing_bootstrap_blocks_before_lifecycle(self):
        chain=Mock(root=drafts.ROOT);flow=drafts.Workflow(chain,None)
        with self.assertRaises(drafts.Blocked):flow.run()
        chain.setup.assert_not_called()
    def test_invalid_mode_and_normal_without_validator_no_transport(self):
        flow=drafts.Workflow(Mock(root=drafts.ROOT),None)
        with patch.object(drafts.promotion,'transport') as wire:
            with self.assertRaises(AssertionError):flow.call('POST','/api/v1/inhibitions/preview',{})
            with self.assertRaises(AssertionError):flow.call('POST','/api/v1/inhibitions/preview',{},mode='mock',validate=lambda x:None)
            wire.assert_not_called()
    def test_failed_business_validator_cannot_register_normal_success(self):
        chain=Mock(root=drafts.ROOT,work=self.work,cookies={'source':'test'});flow=drafts.Workflow(chain,None);flow.contract.shape=Mock()
        def bad(value):raise AssertionError('owned counterexample')
        with patch.object(drafts.promotion,'transport',return_value=(200,b'{}',{})),patch.object(drafts.promotion,'preserve_first_failure'),patch.object(drafts,'emit') as emit:
            with self.assertRaises(AssertionError):flow.call('POST','/api/v1/inhibitions/preview',{},mode='protocol_mock',validate=bad)
            row=emit.call_args.kwargs['interface'];self.assertFalse(row['registration_evidence']);self.assertEqual(row['evidence_mode'],'protocol_mock');self.assertEqual(row['upstream_evidence_mode'],'protocol_mock');self.assertEqual(emit.call_args.args[1],'failed');self.assertEqual(emit.call_args.kwargs['evidence_mode'],'protocol_mock')
        raw=json.loads((self.work/'draft-http-1.json').read_text())
        self.assertEqual((raw['status'],raw['response']),(200,{}));self.assertNotIn('Cookie',raw)
        self.assertEqual((self.work/'draft-http-1.json').stat().st_mode&0o777,0o600)
    def test_scoped_ports_restored_on_exception(self):
        old=(drafts.promotion.PORTS,drafts.promotion.GITLAB_PROXY_PORT,drafts.promotion.GITLAB_PROXY_ORIGIN)
        with self.assertRaises(RuntimeError):
            with drafts.ports():raise RuntimeError('controlled')
        self.assertEqual(old,(drafts.promotion.PORTS,drafts.promotion.GITLAB_PROXY_PORT,drafts.promotion.GITLAB_PROXY_ORIGIN))
    def test_inventory_cli_never_allocates_or_calls_doctor(self):
        with patch.object(drafts.promotion,'doctor') as doctor,patch.object(drafts.promotion.Chain,'setup') as setup,patch.object(drafts.subprocess,'Popen') as popen,contextlib.redirect_stdout(io.StringIO()):self.assertEqual(drafts.main(['--inventory']),0)
        doctor.assert_not_called();setup.assert_not_called();popen.assert_not_called()
    def test_failed_admission_doctor_never_starts_native_or_container(self):
        with patch.object(drafts.promotion,'exclusive',return_value=contextlib.nullcontext()),patch.object(drafts.promotion,'doctor',side_effect=drafts.Blocked('controlled ownership guard')),patch.object(drafts.subprocess,'check_output') as image,patch.object(drafts.promotion.Chain,'setup') as setup,contextlib.redirect_stdout(io.StringIO()):self.assertEqual(drafts.main(['--live']),2)
        image.assert_not_called();setup.assert_not_called()
    def test_real_source_bootstrap_and_synthetic_history_keep_distinct_modes(self):
        query={'from':['2026-09-24T00:00:00Z'],'through':['2026-10-01T00:00:00Z']};v=drafts.history_payload(query,self.app,'owned-app','owned-source')
        self.assertEqual(v['coverage_start'],query['from'][0]);self.assertEqual(v['coverage_end'],query['through'][0]);self.assertEqual([x['labels']['severity'] for x in v['alerts']],['P1','P3']);self.assertTrue(v['complete'])
        with self.assertRaises(AssertionError):drafts.history_payload(dict(query,**{'from':['2026-09-30T00:00:00Z']}),self.app,'owned-app','owned-source')
    def test_receiver_reference_uses_actual_plural_projection(self):
        value={'root':{'receiver_refs':['receiver-1'],'children':[{'receiver_refs':['receiver-2']}]}}
        self.assertEqual(drafts.Sources.find_receiver(None,value),'receiver-1')
        with self.assertRaises(AssertionError):drafts.Sources.find_receiver(None,{'root':{'receiver_refs':['secret-name']}})
    def test_unknown_container_create_outcome_is_never_adopted_or_green(self):
        with tempfile.TemporaryDirectory() as tmp:
            source=object.__new__(drafts.Sources);source.prom_name='own-prom';source.prom_cid=None;source.prom_attempted=True;source.am_name='own-am';source.am_cid=None;source.am_attempted=False;source.work=Path(tmp);source.source=Mock();source.chain=Mock();source.capture_count=0;source.history_requests=[]
            with patch.object(drafts.subprocess,'run',return_value=Mock(returncode=0,stderr='')),self.assertRaises(drafts.Blocked):source.cleanup()
            source.chain.command.assert_not_called();source.source.close.assert_called_once()
            self.assertTrue(json.loads((Path(tmp)/'draft-source-cleanup.json').read_text())['errors'])
    def test_am_foreign_identity_failure_still_closes_owned_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            source=object.__new__(drafts.Sources);source.prom_name='own-prom';source.prom_cid=None;source.prom_attempted=False;source.am_name='own-am';source.am_cid='c'*64;source.am_attempted=True;source.work=Path(tmp);source.source=Mock();source.chain=Mock();source.capture_count=0;source.history_requests=[]
            with patch.object(drafts.ro,'inspect_pg',return_value={}),patch.object(drafts.routes,'guard_am',side_effect=drafts.Blocked('foreign')),self.assertRaises(drafts.Blocked):source.cleanup()
            source.chain.command.assert_not_called();source.source.close.assert_called_once()
    def test_source_bytes_permission_and_symlink_drift_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            source=object.__new__(drafts.Sources);source.prom_cid=source.am_cid=None;source.work=Path(tmp);f=source.work/'cfg';f.write_text('owned');f.chmod(0o600);source.config_hashes={'cfg':drafts.hashlib.sha256(f.read_bytes()).hexdigest()};source.frozen()
            f.chmod(0o644)
            with self.assertRaises(AssertionError):source.frozen()
            f.chmod(0o600);f.write_text('changed')
            with self.assertRaises(AssertionError):source.frozen()
    def test_explicit_mock_mode_preserved_on_passed_http_cell(self):
        chain=Mock(root=drafts.ROOT,work=self.work,cookies={'source':'test'});flow=drafts.Workflow(chain,None);flow.contract.shape=Mock()
        with patch.object(drafts.promotion,'transport',return_value=(200,b'{"proof":1}',{})),patch.object(drafts,'emit') as emit:
            value=flow.call('POST','/api/v1/inhibitions/preview',{},mode='protocol_mock',validate=lambda v:self.assertEqual(v['proof'],1))
            row=emit.call_args.kwargs['interface'];self.assertEqual(row['evidence_mode'],'protocol_mock');self.assertEqual(row['upstream_evidence_mode'],'protocol_mock');self.assertEqual(emit.call_args.kwargs['evidence_mode'],'protocol_mock');self.assertTrue(row['registration_evidence'])
    def test_current_source_tree_uses_prepared_business_reader_before_http(self):
        source=object.__new__(drafts.Sources);source.revision='sha256:'+'a'*64
        chain=Mock(cookies={'source':'platform-admin-cookie'});source.chain=chain
        chain.seed_identities.side_effect=lambda:chain.cookies.update(source='scoped-business-reader-cookie')
        flow=Mock(last_headers={'ETag':'"'+source.revision+'"'})
        def read(method,path,**kwargs):
            self.assertEqual(chain.cookies['source'],'scoped-business-reader-cookie')
            self.assertEqual((method,path),('GET','/api/v1/notification-routes'))
            tree={'root':{'receiver_refs':['receiver-1']}};kwargs['validate'](tree);return tree
        flow.call.side_effect=read
        with patch.object(drafts,'Workflow',return_value=flow):
            self.assertEqual(source.read_current_tree(),('"'+source.revision+'"','receiver-1'))
        chain.seed_identities.assert_called_once()
    def test_current_source_tree_etag_mismatch_cannot_supply_write_revision(self):
        source=object.__new__(drafts.Sources);source.revision='sha256:'+'a'*64;source.chain=Mock()
        flow=Mock(last_headers={'ETag':'"sha256:'+'b'*64+'"'})
        flow.call.return_value={'root':{'receiver_refs':['receiver-1']}}
        with patch.object(drafts,'Workflow',return_value=flow),self.assertRaisesRegex(AssertionError,'ETag differs'):
            source.read_current_tree()
    def test_put_drop_is_bounded_to_registered_held_native_draft(self):
        proxy=object.__new__(drafts.DraftTLSProxy);proxy.tier='source';proxy.lock=threading.Lock();proxy.drop=None;proxy.drop_receipt=None
        proc=Mock();proc.poll.return_value=None
        chain=Mock(native_rule_draft='c'*32,processes={'source':proc},proxies={'source':proxy});proxy.chain=chain
        for method,path in [('POST','/api/v1/alert-rules/'+'c'*32),('PUT','/api/v1/alert-rules/'+'d'*32),('PUT','/api/v1/config-sync/mr-1/import')]:
            with self.assertRaises(drafts.Blocked):proxy.arm_drop(method,path)
        self.assertIsNone(proxy.drop)
        proxy.arm_drop('PUT','/api/v1/alert-rules/'+'c'*32)
        self.assertEqual(proxy.drop,('PUT','/api/v1/alert-rules/'+'c'*32))
        with self.assertRaises(drafts.Blocked):proxy.arm_drop('PUT','/api/v1/alert-rules/'+'c'*32)
        proxy.drop=None;proc.poll.return_value=0
        with self.assertRaises(drafts.Blocked):proxy.arm_drop('PUT','/api/v1/alert-rules/'+'c'*32)
        proc.poll.return_value=None;chain.proxies.clear()
        with self.assertRaises(drafts.Blocked):proxy.arm_drop('PUT','/api/v1/alert-rules/'+'c'*32)
    def test_source_and_workflow_http_names_and_raw_never_collide(self):
        chain=Mock(root=drafts.ROOT,work=self.work,cookies={'source':'test'})
        first=drafts.Workflow(chain,None);second=drafts.Workflow(chain,None)
        first.contract.shape=second.contract.shape=Mock()
        with patch.object(drafts.promotion,'transport',return_value=(200,b'{}',{})),patch.object(drafts,'emit') as emit:
            for flow in (first,second):flow.call('GET','/api/v1/notification-routes',validate=lambda value:None)
        self.assertEqual([c.args[0] for c in emit.call_args_list],['drafts.http.1','drafts.http.2'])
        self.assertTrue((self.work/'draft-http-1.json').is_file());self.assertTrue((self.work/'draft-http-2.json').is_file())
    def test_authorization_counterexamples_have_valid_required_write_headers(self):
        chain=Mock(root=drafts.ROOT,run_id=self.nonce,denied_cookie='denied',reader_role='reader',databases={'source':'own-db'});flow=drafts.Workflow(chain,None)
        flow.snapshot=Mock(return_value={'native_state':'unchanged'});flow.call=Mock();flow.mark=Mock()
        revision='"sha256:'+'a'*64+'"';source={'receiver_ref':'receiver-1','inhibition_etag':revision,'route_etag':revision}
        flow.authorization(self.spec,'c'*32,source)
        self.assertEqual(flow.call.call_count,20)
        for call in flow.call.call_args_list:
            path=call.args[1]
            if path.endswith('/preview'):self.assertNotIn('key',call.kwargs)
            else:self.assertTrue(call.kwargs['key'].startswith('authorization-'+self.nonce))
            if path in ('/api/v1/inhibitions','/api/v1/notification-routes'):
                self.assertEqual(call.kwargs['revision'],revision)
        self.assertEqual(sum(c.kwargs['want']==403 for c in flow.call.call_args_list),10)
        self.assertEqual(sum(c.kwargs['want']==422 for c in flow.call.call_args_list),5)
if __name__=='__main__':unittest.main()

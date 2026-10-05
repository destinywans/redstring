"""Offline contract and lifecycle counterexamples, no resource acquisition."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_workspace_audit_rollback_coverage as c
import api_management_chain as shared
class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        chain=c.Chain.__new__(c.Chain);chain.work=self.work;chain.nonce='a'*32;chain.actor='b'*32;chain.fault_attempted=False
        chain.database='aiops_testlab_management_'+chain.nonce;chain.db_user='management_'+chain.nonce;chain.owner_tag='owner:uuid'
        chain.frozen=Mock();chain.owner=Mock();chain.sql=Mock();chain.raw_number=0
        return chain
    def data(self):
        expected=dict(name='New UUID Workspace',slug='test',timezone='Asia/Shanghai',locale='en-US',week_start='monday',retention_days=90,data_region='local',brand={'name':'original','logo':None,'pending':False})
        row=dict(id='default',name='Old',v3_slug='test',timezone='Asia/Shanghai',language='en-US',v3_week_start='monday',v3_retention_days=90,v3_data_region='local',v3_brand_name='original',v3_brand_logo=None,v3_brand_pending=False,revision=2,updated_at='2026-10-05T00:00:00+00:00',other='preserved')
        before=dict(workspace=row,business={'other':'hash'},audits=[dict(id=1,old='preserved')]);after=copy.deepcopy(before)
        after['workspace'].update(name=expected['name'],revision=3,updated_at='2026-10-05T00:00:01+00:00')
        after['audits'].append(dict(id=2,actor_id='b'*32,actor_kind='user',application_id='',action='v3.workspace.updated',target='default',outcome='success',changes=dict(revision_before=2,revision_after=3,**{k:expected[k] for k in c.PREFIELDS})))
        return before,after,expected
    def test_counterexample_wrong_audit_error_then_precise_source_code(self):
        source=(c.ROOT/'internal/v3settings/workspace.go').read_text();default=source.split('default:',1)[1]
        # Old generic audit_unavailable expectation is red; actual default maps settings_unavailable.
        with self.assertRaises(AssertionError):self.assertIn('"audit_unavailable"',default)
        self.assertIn('"settings_unavailable"',default)
        chain=self.chain();before,_,payload=self.data();chain.payload=payload;chain.etag='"2"';chain.expected=dict(payload,name='Old');chain.ledger=Mock(return_value=before)
        chain.install_fault=Mock();chain.remove_fault=Mock();chain.call=Mock(side_effect=AssertionError('stop after exact wire'));chain.secondary=Mock()
        with self.assertRaises(AssertionError):chain.exercise(before)
        self.assertEqual(chain.call.call_args.kwargs['code'],'settings_unavailable')
    def test_full_rollback_rejects_row_revision_timestamp_and_any_ledger_residue(self):
        before,after,_=self.data();c.verify_rollback(before,copy.deepcopy(before))
        for key in ('revision','updated_at','v3_brand_name','other'):
            bad=copy.deepcopy(before);bad['workspace'][key]='changed'
            with self.assertRaises(AssertionError):c.verify_rollback(before,bad)
        for key,value in [('business',{}),('audits',after['audits'])]:
            with self.assertRaises(AssertionError):c.verify_rollback(before,dict(before,**{key:value}))
    def test_exact_recovery_one_revision_one_audit_and_immutable_row(self):
        before,after,expected=self.data();c.verify_update(before,after,'b'*32,expected)
        for key,value in [('revision',4),('name','lost'),('v3_brand_pending',True),('other','lost'),('updated_at',before['workspace']['updated_at'])]:
            bad=copy.deepcopy(after);bad['workspace'][key]=value
            with self.assertRaises(AssertionError):c.verify_update(before,bad,'b'*32,expected)
        bad=copy.deepcopy(after);bad['business']['other']='changed'
        with self.assertRaises(AssertionError):c.verify_update(before,bad,'b'*32,expected)
    def test_full_audit_prefix_count_actor_revision_and_no_env_guess(self):
        before,after,expected=self.data()
        for rows in (after['audits'][1:],after['audits'][:-1],after['audits']+[dict(after['audits'][-1],id=3)]):
            with self.assertRaises(AssertionError):c.verify_update(before,dict(after,audits=rows),'b'*32,expected)
        for key,value in [('actor_id','other'),('target','other'),('application_id','app'),('outcome','failure'),('changes',dict(after['audits'][-1]['changes'],environment_id='test'))]:
            bad=copy.deepcopy(after);bad['audits'][-1][key]=value
            with self.assertRaises(AssertionError):c.verify_update(before,bad,'b'*32,expected)
    def test_trigger_exact_actor_action_target_empty_app_success(self):
        _,create,drop=c.fault_statements('a'*32,'b'*32)
        for v in ("NEW.actor_id='"+'b'*32+"'","NEW.action='v3.workspace.updated'","NEW.target='default'","NEW.outcome='success'","NEW.application_id=''"):
            self.assertIn(v,create)
        self.assertNotIn('environment_id',create);self.assertIn('BEGIN;',create);self.assertTrue(create.endswith('COMMIT;'));self.assertIn('DROP TRIGGER IF EXISTS',drop)
        with self.assertRaises(AssertionError):c.fault_statements('a'*32,"x';DROP")
    def test_install_unknown_commit_keeps_restore_intent(self):
        chain=self.chain();chain.sql.side_effect=RuntimeError('unknown commit')
        with self.assertRaises(RuntimeError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual((self.work/'workspace-fault-intent.json').stat().st_mode&0o777,0o600)
    def test_intent_io_failure_prevents_ddl_but_keeps_safe_flag(self):
        chain=self.chain()
        with patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);chain.sql.assert_not_called()
    def test_restore_owner_drop_absence_share_deadline_and_no_source_guard(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':False,'function':False})])
        with patch.object(c.time,'monotonic',return_value=100):chain.remove_fault(deadline=102)
        chain.owner.assert_called_once_with(deadline=102);self.assertEqual([r.kwargs['deadline'] for r in chain.pg.call_args_list],[102,102]);self.assertFalse(chain.fault_attempted);chain.frozen.assert_not_called()
    def test_remaining_fault_keeps_intent_no_retry(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':True,'function':False})])
        with self.assertRaises(AssertionError):chain.remove_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual(chain.pg.call_count,2)
    def test_cleanup_single40_with_restore_inside4(self):
        chain=self.chain();chain.remove_fault=Mock()
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;chain.cleanup()
        self.assertEqual(chain.cleanup_deadline,140);chain.remove_fault.assert_called_once_with(deadline=104);cleanup.assert_called_once_with(chain)
        self.assertIn('cleanup_deadline',c.bounded_cleanup.__code__.co_names)
    def test_restore_failure_still_pgcleanup_and_not_green(self):
        chain=self.chain();chain.remove_fault=Mock(side_effect=RuntimeError('restore first'))
        with patch.object(c,'bounded_cleanup') as cleanup,patch.object(c,'emit') as emit:
            with self.assertRaisesRegex(RuntimeError,'restore first'):chain.cleanup()
        cleanup.assert_called_once_with(chain);emit.assert_not_called()
    def test_both_cleanup_errors_and_evidence_failure_preserve_first(self):
        chain=self.chain();chain.remove_fault=Mock(side_effect=RuntimeError('restore first'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with patch.object(c,'bounded_cleanup',side_effect=AssertionError('PG second')) as cleanup:
            with self.assertRaisesRegex(RuntimeError,'restore first'):chain.cleanup()
        cleanup.assert_called_once_with(chain)
    def test_first_wire_failure_survives_finally_no_recovery(self):
        chain=self.chain();chain.payload={};chain.etag='"2"';chain.install_fault=Mock();chain.call=Mock(side_effect=AssertionError('HTTP first'))
        chain.remove_fault=Mock(side_effect=RuntimeError('restore second'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with self.assertRaisesRegex(AssertionError,'HTTP first'):chain.exercise({})
        chain.call.assert_called_once();chain.remove_fault.assert_called_once()
    def test_same_body_etag_recovery_otherAPI_no_retry(self):
        chain=self.chain();before,after,payload=self.data();chain.payload=payload;chain.expected=dict(payload,name='Old');chain.etag='"2"'
        chain.ledger=Mock(side_effect=[before,before,after,after]);chain.save_ledger=Mock();chain.install_fault=Mock();chain.remove_fault=Mock();chain.read=Mock();chain.marker_case=Mock()
        chain.call=Mock(side_effect=[({},{}),(payload,{'ETag':'"3"'})]);chain.exercise(before)
        first,last=chain.call.call_args_list;self.assertIs(first.args[3],last.args[3]);self.assertEqual(first.kwargs['headers'],last.kwargs['headers']);self.assertEqual([first.kwargs['index'],last.kwargs['index']],[0,1]);self.assertEqual(chain.call.call_count,2)
        self.assertEqual(chain.read.call_count,4)
    def test_failed_readback_etag_or_cache_mismatch_rejected(self):
        chain=self.chain()
        for headers in ({'ETag':'"3"','Cache-Control':'private, no-store'},{'ETag':'"2"','Cache-Control':'public'}):
            chain.call=Mock(return_value=({},headers))
            with self.assertRaises(AssertionError):chain.read({},'"2"',1)
    def test_raw_owned_receipt_has_hash_body_and_no_cookie(self):
        chain=self.chain();payload={'name':'synthetic'}
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[1],'PUT','/api/v1/workspace',json.dumps(payload).encode(),{'If-Match':'"2"','Cookie':'secret-not-saved'})
        with patch.object(c.base.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(503,b'{"error":{"code":"settings_unavailable"}}',{'Set-Cookie':'secret-not-saved'})) as transport:
            chain.call('admin','PUT','/api/v1/workspace',payload)
            self.assertIs(c.base.transport,transport)
        raw=(self.work/'workspace-wire-1.json').read_text();self.assertNotIn('secret-not-saved',raw);v=json.loads(raw)
        self.assertEqual(v['If_Match'],'"2"');self.assertEqual(len(v['response_sha256']),64);self.assertEqual(v['API_index'],1)
    def test_raw_evidence_failure_restores_original_transport(self):
        chain=self.chain()
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[0],'GET','/api/v1/workspace')
        with patch.object(c.base.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(200,b'{}',{})) as transport,patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.call('reader','GET','/api/v1/workspace')
            self.assertIs(c.base.transport,transport)
    def test_ledger_full_allrows_including_other_workspace_rows_readonly(self):
        chain=self.chain();chain.sql.side_effect=['["sessions","v3_runbooks"]','{}'];chain.ledger();query=chain.sql.call_args.args[0]
        self.assertIn('json_object_agg',query);self.assertIn('to_jsonb(t)',query);self.assertIn('workspace_settings_other',query);self.assertIn("id<>'default'",query);self.assertTrue(chain.sql.call_args.kwargs['read'])
    def test_private_passed_management_globals_unchanged(self):
        self.assertIsNot(c.base,shared);self.assertEqual(shared.PORTS,(18606,18607));self.assertEqual(c.base.PORTS,(18832,18833));self.assertNotEqual(shared.TARGETS,c.base.TARGETS)
    def test_bounded_contract_rejects_other_routes_query_and_absolute(self):
        contract=c.Contract(c.ROOT)
        for method,path in [('GET','/api/v1/workspace'),('PUT','/api/v1/workspace'),('POST','/api/v1/auth/login')]:contract.canonical(method,path)
        for method,path in [('POST','/api/v1/services'),('GET','/api/v1/workspace?x=1'),('GET','https://external/api/v1/workspace')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
    def test_source_drift_stops_before_transport(self):
        chain=self.chain();chain.frozen.side_effect=c.Blocked('source drift')
        with patch.object(c.base.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):chain.call('reader','GET','/api/v1/workspace')
        wire.assert_not_called()
    def test_exact_pg_inspection_always_rechecks(self):
        with patch.object(c,'_inspect',return_value={'Id':'wrong'}):
            with self.assertRaises(AssertionError):c.base.inspect_pg('owned')
    def test_inventory_and_missing_live_never_acquire(self):
        with patch.object(c,'doctor') as doctor,patch.object(c.os,'open') as opened,patch.object(c.sqlite3,'connect') as sqlite,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(c.main(['--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['estimated_HTTP_calls'],11);doctor.assert_not_called();opened.assert_not_called();sqlite.assert_not_called()
        with patch.object(c,'admission') as admit,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admit.assert_not_called()
    def permit(self):
        run=str(uuid.uuid4());permit=dict(status='root-admitted-workspace-audit-rollback',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,
            runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},manifest_sha256='c'*64,
            fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,workspace_audit_fault_authorized=True)
        row=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='c'*64,pid=os.getppid());return permit,row
    def admit(self,permit,rows):
        path=c.ROOT/'.local/testlab'/('offline-'+permit['resource_run_id']+'.json')
        with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=os.getuid()))()),patch.object(c.base,'private_json',return_value=permit),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as db:
            db.return_value.execute.return_value=[(json.dumps(r),) for r in rows];return c.admission(path,permit['resource_run_id'])
    def test_root_sole_matching_runner_source_manifest_parent(self):
        permit,row=self.permit();self.admit(permit,[row])
        for rows in ([],[row,row],[dict(row,pid=os.getppid()+1)],[dict(row,selected_suite='other')],[dict(row,manifest_sha='d'*64)],[dict(row,source_tree_sha='d'*64)]):
            with self.assertRaises(AssertionError):self.admit(permit,rows)
    def test_root_identity_and_fault_permission_drift_rejected(self):
        permit,row=self.permit()
        for key in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','fresh_nonce_hmac_process_tuple_verified','headroom_verified','workspace_audit_fault_authorized'):
            bad=copy.deepcopy(permit);bad[key]=False
            with self.assertRaises(AssertionError):self.admit(bad,[row])
if __name__=='__main__':unittest.main()

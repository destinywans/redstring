"""Pure offline counterexamples; no HTTP/PG/Docker or resource doctor calls."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_knowledge_probe_result_audit_coverage as c
import api_knowledge_sources_interfaces_coverage as shared

class ProbeAuditTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        chain=c.Chain.__new__(c.Chain);chain.work=self.work;chain.nonce='a'*32;chain.app='app-owned';chain.users={'reader':'b'*32}
        chain.database='aiops_testlab_readonly_'+chain.nonce;chain.db_user='readonly_'+chain.nonce;chain.owner_tag='owner:uuid'
        chain.frozen=Mock();chain.owner=Mock();chain.sql=Mock();chain.fault_attempted=False
        return chain
    def row(self,outcome,id=2):
        return dict(id=id,actor_id='b'*32,actor_kind='user',application_id='app-owned',action='v3.knowledge_source.connection.test',
            target='upload',outcome=outcome,changes={'environment_id':'test','team_id':''},created_at='current')
    def ledgers(self,outcomes):
        before=dict(business={'v3_uploads':'byte-hash','v3_runbooks':'row-hash','source':'config-hash'},audits=[dict(id=1,old=True)])
        after=copy.deepcopy(before);after['audits'] += [self.row(v,i+2) for i,v in enumerate(outcomes)]
        return before,after
    def test_counterexample_whole_audit_rollback_is_wrong_but_one_attempt_passes(self):
        before,after=self.ledgers(['attempted'])
        with self.assertRaises(AssertionError):self.assertEqual(before,after) # Reproduce wrong full-rollback assertion before the corrected contract.
        c.assert_audits(before,after,['attempted'],'b'*32,'app-owned')
        with self.assertRaises(AssertionError):c.assert_audits(before,before,['attempted'],'b'*32,'app-owned')
    def test_success_or_failure_result_on_failed_request_is_rejected(self):
        for outcomes in (['attempted','success'],['attempted','failure'],['success'],[]):
            before,after=self.ledgers(outcomes)
            with self.assertRaises(AssertionError):c.assert_audits(before,after,['attempted'],'b'*32,'app-owned')
    def test_any_business_bytes_sync_or_runbook_residue_rejected(self):
        before,after=self.ledgers(['attempted'])
        for key in after['business']:
            bad=copy.deepcopy(after);bad['business'][key]='changed'
            with self.assertRaises(AssertionError):c.assert_audits(before,bad,['attempted'],'b'*32,'app-owned')
    def test_complete_audit_prefix_and_scope_are_strict(self):
        before,after=self.ledgers(['attempted'])
        bad=copy.deepcopy(after);bad['audits'][0]['old']=False
        with self.assertRaises(AssertionError):c.assert_audits(before,bad,['attempted'],'b'*32,'app-owned')
        for key,value in [('actor_id','other'),('actor_kind','token'),('application_id','foreign'),('target','web'),('action','other'),('changes',{'secret_ref':'leak'})]:
            bad=copy.deepcopy(after);bad['audits'][-1][key]=value
            with self.assertRaises(AssertionError):c.assert_audits(before,bad,['attempted'],'b'*32,'app-owned')
    def test_recovery_is_two_new_audits_not_request_deduplication(self):
        before,failed=self.ledgers(['attempted']);_,recovered=self.ledgers(['attempted','attempted','success'])
        c.assert_audits(failed,recovered,['attempted','success'],'b'*32,'app-owned')
        c.assert_audits(before,recovered,['attempted','attempted','success'],'b'*32,'app-owned')
        with self.assertRaises(AssertionError):c.assert_audits(failed,failed,['attempted','success'],'b'*32,'app-owned')
    def test_fault_only_exact_second_success_upload_scope(self):
        name,create,drop=c.fault_statements('a'*32,'b'*32,'app-owned')
        for needle in ("NEW.outcome='success'","NEW.target='upload'","NEW.actor_id='"+'b'*32+"'","NEW.application_id='app-owned'", "NEW.action='v3.knowledge_source.connection.test'", "NEW.changes=jsonb_build_object('environment_id','test','team_id','')"):
            self.assertIn(needle,create)
        self.assertNotIn("NEW.outcome='attempted'",create);self.assertIn('BEGIN;',create);self.assertTrue(create.endswith('COMMIT;'))
        self.assertIn('IF EXISTS '+name,drop);self.assertNotIn('INSERT INTO',create)
        for args in [('bad','b'*32,'app'),('a'*32,'invalid','app'),('a'*32,'b'*32,"x';DROP")]:
            with self.assertRaises(AssertionError):c.fault_statements(*args)
    def test_unknown_install_intent_is_retained_before_ddl(self):
        chain=self.chain();chain.sql.side_effect=RuntimeError('unknown commit')
        with self.assertRaises(RuntimeError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);self.assertTrue((self.work/'probe-fault-intent.json').exists())
        self.assertEqual((self.work/'probe-fault-intent.json').stat().st_mode&0o777,0o600)
    def test_evidence_failure_prevents_install_but_preserves_safe_intent(self):
        chain=self.chain()
        with patch.object(c.ro,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);chain.sql.assert_not_called()
    def test_remove_uses_same_four_second_deadline_owner_drop_readback(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':False,'function':False})])
        with patch.object(c.time,'monotonic',return_value=100):chain.remove_fault(deadline=102)
        chain.owner.assert_called_once_with(deadline=102)
        self.assertEqual([v.kwargs['deadline'] for v in chain.pg.call_args_list],[102,102]);self.assertFalse(chain.fault_attempted)
        chain.frozen.assert_not_called() # Source drift never prevents exact-owned restoration.
    def test_restore_readback_failure_keeps_intent_without_retry(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':True,'function':False})])
        with self.assertRaises(AssertionError):chain.remove_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual(chain.pg.call_count,2)
    def test_owner_readonly_exact_uuid_deadline(self):
        chain=self.chain();del chain.owner
        chain.pg=Mock(return_value=json.dumps(dict(database=chain.database,role=chain.db_user,db_tag=chain.owner_tag,role_tag=chain.owner_tag)))
        chain.owner(deadline=104);self.assertIn('BEGIN READ ONLY;',chain.pg.call_args.args[0]);self.assertEqual(chain.pg.call_args.kwargs,{'deadline':104})
        chain.pg.return_value='{}'
        with self.assertRaises(AssertionError):chain.owner(deadline=104)
    def test_cleanup_restore_and_original_pg_share_one40_deadline(self):
        chain=self.chain();chain.remove_fault=Mock()
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;chain.cleanup()
        chain.remove_fault.assert_called_once_with(deadline=104);self.assertEqual(chain.cleanup_deadline,140);cleanup.assert_called_once_with(chain)
    def test_restore_failure_still_runs_pg_cleanup_and_does_not_emit_pass(self):
        chain=self.chain();chain.remove_fault=Mock(side_effect=RuntimeError('restore red'))
        with patch.object(c,'bounded_cleanup') as cleanup,patch.object(c,'emit') as emit:
            with self.assertRaisesRegex(RuntimeError,'restore red'):chain.cleanup()
        cleanup.assert_called_once_with(chain);emit.assert_not_called()
    def test_first_http_failure_survives_restore_failure_no_recovery_request(self):
        chain=self.chain();chain.payload={'type':'upload'};chain.ledger=Mock(return_value={});chain.install_fault=Mock()
        chain.call=Mock(side_effect=AssertionError('original HTTP red'));chain.remove_fault=Mock(side_effect=RuntimeError('restore red'));chain.diagnostic=Mock()
        with self.assertRaisesRegex(AssertionError,'original HTTP red'):chain.exercise()
        chain.call.assert_called_once();chain.remove_fault.assert_called_once();chain.diagnostic.assert_called_once()
    def test_secondary_diagnostic_io_never_replaces_first_error(self):
        chain=self.chain();chain.diagnostic=Mock(side_effect=OSError('full'));primary=AssertionError('first HTTP red')
        chain.secondary(self.work/'secondary.json',RuntimeError('restore red'),primary)
        self.assertEqual(str(primary),'first HTTP red');self.assertIn('OSError',primary.__notes__[0])
    def test_both_cleanup_errors_preserve_restore_first_even_if_evidence_io_fails(self):
        chain=self.chain();chain.remove_fault=Mock(side_effect=RuntimeError('restore first'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with patch.object(c,'bounded_cleanup',side_effect=AssertionError('PG second')) as cleanup:
            with self.assertRaisesRegex(RuntimeError,'restore first'):chain.cleanup()
        cleanup.assert_called_once_with(chain)
    def test_fault_and_recovery_only_same_payload_other_api(self):
        chain=self.chain();chain.payload={'type':'upload','url':'owned'};before,failed=self.ledgers(['attempted']);_,recovered=self.ledgers(['attempted','attempted','success'])
        chain.ledger=Mock(side_effect=[before,failed,recovered]);chain.install_fault=Mock();chain.remove_fault=Mock();chain.call=Mock();chain.marker_case=Mock()
        chain.exercise();self.assertEqual(chain.call.call_count,2)
        first,last=chain.call.call_args_list
        self.assertIs(first.args[3],last.args[3]);self.assertEqual(first.kwargs['want'],503);self.assertEqual(first.kwargs['code'],'audit_unavailable')
        self.assertEqual([first.kwargs['index'],last.kwargs['index']],[0,1]);self.assertIs(last.kwargs['validate'],c.base.probe_result)
    def test_private_copy_does_not_change_passed_helper_globals(self):
        self.assertIsNot(c.base,shared);self.assertIsNot(c.ro,shared.ro)
        self.assertEqual(shared.PORTS,(18714,18715));self.assertEqual(c.ro.PORTS,(18820,18821))
    def test_inventory_does_not_allow_register_sync_revoke_or_external_url(self):
        contract=c.Contract(c.ROOT)
        for method,path in (*c.TARGETS,*c.SUPPORT):contract.canonical(method,'/api/v1'+path)
        for method,path in [('POST','/api/v1/knowledge-sources'),('POST','/api/v1/runbooks'),('GET','https://external/api/v1/knowledge-sources/test'),('POST','/api/v1/knowledge-sources/test?x=1')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
    def test_opt_in_blocks_before_lease_or_doctor(self):
        with patch.object(c,'admission') as admit,patch.object(c.base,'doctor') as doctor,patch.object(c.os,'open') as opened,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admit.assert_not_called();doctor.assert_not_called();opened.assert_not_called()
    def test_source_drift_blocks_before_wire(self):
        chain=self.chain();chain.frozen.side_effect=c.Blocked('source drift')
        with patch.object(c.ro.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):chain.call('reader','POST','/api/v1/knowledge-sources/test',{})
        wire.assert_not_called()
    def test_all_table_digest_not_json_build_object_argument_limit(self):
        chain=self.chain();tables=['v3_uploads','v3_upload_revocations','v3_knowledge_source_registrations','v3_knowledge_source_reads','v3_knowledge_source_observations','v3_external_source_documents','v3_external_source_syncs','v3_runbooks']+['extra_'+str(i) for i in range(120)]
        chain.sql.side_effect=[json.dumps(tables),json.dumps({'business':{},'audits':[]})];chain.ledger()
        statement=chain.sql.call_args.args[0];self.assertIn('json_object_agg(name,digest)',statement);self.assertIn('UNION ALL',statement);self.assertIn('to_jsonb(t)',statement)
        self.assertEqual(statement.count(' AS name,'),len(tables));self.assertTrue(chain.sql.call_args.kwargs['read'])
    def permit(self):
        run=str(uuid.uuid4());path=c.ROOT/'.local/testlab'/('offline-only-'+run+'.json')
        permit=dict(status='root-admitted-knowledge-probe-result-audit',owner=c.ro.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,
            binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},
            fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,second_result_audit_fault_authorized=True,manifest_sha256='c'*64)
        active=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='c'*64,pid=os.getppid())
        return run,path,permit,active
    def admit(self,permit,active):
        run,path,_,_=self.permit();run=permit['resource_run_id'];path=c.ROOT/'.local/testlab'/('offline-only-'+run+'.json')
        with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=os.getuid()))()),patch.object(c.ro,'private_json',return_value=permit),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as sql:
            sql.return_value.execute.return_value=[(json.dumps(r),) for r in active]
            return c.admission(path,run)
    def test_admission_requires_unique_matching_runner_hash_source_parent(self):
        run,path,permit,active=self.permit();self.admit(permit,[active])
        for rows in ([],[active,active],[dict(active,selected_suite='other')],[dict(active,manifest_sha='d'*64)],[dict(active,source_tree_sha='d'*64)],[dict(active,pid=os.getppid()+1)]):
            with self.assertRaises(AssertionError):self.admit(permit,rows)
    def test_admission_rejects_stale_boolean_and_any_identity_drift(self):
        _,_,permit,active=self.permit()
        for key in ('headroom_verified','fresh_nonce_hmac_process_tuple_verified','second_result_audit_fault_authorized','helper_sha256','ports','postgres_cid','binary_sha256','runtime_sha256','source_tree_sha'):
            bad=copy.deepcopy(permit);bad[key]=False
            with self.assertRaises(AssertionError):self.admit(bad,[active])
    def test_inventory_never_opens_ledger_or_acquires_resources(self):
        with patch.object(c.base,'doctor') as doctor,patch.object(c.sqlite3,'connect') as sql,patch.object(c.os,'open') as opened,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(c.main(['--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['http_calls'],6);doctor.assert_not_called();sql.assert_not_called();opened.assert_not_called()

if __name__=='__main__':unittest.main()

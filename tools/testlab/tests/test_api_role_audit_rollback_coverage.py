"""Offline role contract/atomicity/lifecycle counterexamples; never acquires resources."""
import ast,contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_role_audit_rollback_coverage as c
import api_management_chain as shared
class RoleTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        chain=c.Chain.__new__(c.Chain);chain.work=self.work;chain.nonce='a'*32;chain.actor='b'*32;chain.fault_attempted=False
        chain.database='aiops_testlab_management_'+chain.nonce;chain.db_user='management_'+chain.nonce;chain.owner_tag='owner:uuid'
        chain.roles={'target':'management-'+chain.nonce+'-target'};chain.path='/api/v1/roles/'+chain.roles['target']
        chain.frozen=Mock();chain.owner=Mock();chain.sql=Mock();chain.raw_number=0
        return chain
    def data(self):
        target='management-'+'a'*32+'-target'
        matrix=[dict(resource=code,grants=[0]*5) for code in c.CODES];matrix[1]['grants'][0]=1
        row=dict(id=target,name='Owned target',v3_kind='business',v3_matrix_revision=1,created_at='2026-10-06T00:00:00+00:00',description='preserved')
        grant=dict(role_id=target,module='environment',action='view',scope_kind='global',scope_id='*',assurance='standard',created_at='2026-10-06T00:00:00+00:00')
        before=dict(role=row,roles=[row,dict(id='other',v3_matrix_revision=8)],grants=[grant],business={'sessions':'hash','rbac_user_roles':'hash'},audits=[dict(id=1,old='preserved')]);after=copy.deepcopy(before)
        after['role']['v3_matrix_revision']=2;after['roles'][0]['v3_matrix_revision']=2
        for module in ('members','roles'):after['grants'].append(dict(grant,module=module,created_at='2026-10-06T00:00:01+00:00'))
        wanted=copy.deepcopy(matrix);wanted[10]['grants'][0]=1
        after['audits'].append(dict(id=2,actor_id='b'*32,actor_kind='user',application_id='',action='v3.role.matrix.updated',target=target,outcome='success',changes={'before':c.codes(matrix),'after':c.codes(wanted)}))
        return before,after,matrix,wanted
    def verify(self,before,after,matrix,wanted):return c.verify_update(before,after,'b'*32,'management-'+'a'*32+'-target',matrix,wanted)
    def test_install_unknown_commit_keeps_restore_intent(self):
        chain=self.chain();chain.sql.side_effect=RuntimeError('unknown commit')
        with self.assertRaises(RuntimeError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual((self.work/'role-fault-intent.json').stat().st_mode&0o777,0o600)

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

    def test_exact_pg_inspection_always_rechecks(self):
        with patch.object(c,'_inspect',return_value={'Id':'wrong'}):
            with self.assertRaises(AssertionError):c.base.inspect_pg('owned')

    def permit(self):
        run=str(uuid.uuid4());permit=dict(status='root-admitted-role-audit-rollback',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,
            runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},manifest_sha256='c'*64,
            fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,role_audit_fault_authorized=True)
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
        for key in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','fresh_nonce_hmac_process_tuple_verified','headroom_verified','role_audit_fault_authorized'):
            bad=copy.deepcopy(permit);bad[key]=False
            with self.assertRaises(AssertionError):self.admit(bad,[row])

    def test_full_role_row_rollback_rejects_revision_metadata_scope_grants(self):
        before,after,_,_=self.data();c.verify_rollback(before,copy.deepcopy(before))
        for mutate in (lambda d:d['role'].update(v3_matrix_revision=2),lambda d:d['role'].update(description='lost'),lambda d:d['grants'][0].update(scope_id='foreign'),lambda d:d['grants'][0].update(assurance='passkey'),lambda d:d['grants'][0].update(created_at='changed'),lambda d:d['roles'].append({'id':'rogue'})):
            bad=copy.deepcopy(before);mutate(bad)
            with self.assertRaises(AssertionError):c.verify_rollback(before,bad)
    def test_partial_counterexample_revision_and_count_alone_miss_scope_residue(self):
        before,_,_,_=self.data();bad=copy.deepcopy(before);bad['grants'][0]['scope_id']='foreign'
        self.assertEqual(before['role']['v3_matrix_revision'],bad['role']['v3_matrix_revision']);self.assertEqual(len(before['audits']),len(bad['audits']))
        with self.assertRaises(AssertionError):c.verify_rollback(before,bad)
    def test_update_exact_one_role_and_revision(self):
        before,after,old,new=self.data();self.verify(before,after,old,new)
        for key,value in (('v3_matrix_revision',3),('v3_kind','system'),('description','lost')):
            bad=copy.deepcopy(after);bad['role'][key]=value
            with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
        bad=copy.deepcopy(after);bad['roles'][1]['v3_matrix_revision']=9
        with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
    def test_update_rejects_old_grant_timestamp_or_scope_mutation(self):
        before,after,old,new=self.data()
        for key,value in (('created_at','changed'),('assurance','passkey'),('scope_kind','environment'),('scope_id','test')):
            bad=copy.deepcopy(after);bad['grants'][0][key]=value
            with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
    def test_update_rejects_missing_duplicate_and_wrong_grouped_grants(self):
        before,after,old,new=self.data()
        for rows in (after['grants'][:-1],after['grants']+[after['grants'][-1]],after['grants'][:1]):
            with self.assertRaises(AssertionError):self.verify(before,dict(after,grants=rows),old,new)
        for key,value in (('module','audit'),('role_id','other'),('action','manage'),('scope_id','test'),('assurance','passkey')):
            bad=copy.deepcopy(after);bad['grants'][-1][key]=value
            with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
    def test_new_grant_timestamp_and_unknown_schema_fail_closed(self):
        before,after,old,new=self.data()
        for key,value in (('created_at','2026-10-06T00:00:01'),('unexpected','value')):
            bad=copy.deepcopy(after);bad['grants'][-1][key]=value
            with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
    def test_audit_prefix_actor_target_outcome_and_stable_codes(self):
        before,after,old,new=self.data()
        for key,value in (('actor_id','other'),('target','admin'),('outcome','failure'),('application_id','app'),('action','other'),('actor_kind','machine'),('changes',{})):
            bad=copy.deepcopy(after);bad['audits'][-1][key]=value
            with self.assertRaises(AssertionError):self.verify(before,bad,old,new)
        for rows in (after['audits'][1:],after['audits'][:-1],after['audits']+[after['audits'][-1]]):
            with self.assertRaises(AssertionError):self.verify(before,dict(after,audits=rows),old,new)
    def test_unrelated_public_hash_mutation_rejected(self):
        before,after,old,new=self.data();after['business']['sessions']='changed'
        with self.assertRaises(AssertionError):self.verify(before,after,old,new)
    def test_only_identity_view_cell_may_change(self):
        before,after,old,new=self.data();new[1]['grants'][1]=1
        with self.assertRaises(AssertionError):self.verify(before,after,old,new)
        with self.assertRaises(AssertionError):c.codes(new[:2])
    def test_exact_fault_actor_action_target_success_and_transaction(self):
        name,create,drop=c.fault_statements('a'*32,'b'*32)
        for field in ("NEW.actor_id='"+'b'*32+"'","NEW.target='management-"+'a'*32+"-target'","NEW.action='v3.role.matrix.updated'","NEW.outcome='success'","NEW.application_id=''"):
            self.assertIn(field,create)
        self.assertTrue(create.startswith('BEGIN;'));self.assertTrue(create.endswith('COMMIT;'));self.assertIn(name,drop)
        for actor in ('admin',"x';DROP"):
            with self.assertRaises(AssertionError):c.fault_statements('a'*32,actor)
    def test_failure_and_recovery_same_payload_revision_different_API(self):
        chain=self.chain();before,after,old,new=self.data();chain.payload={'matrix':new};chain.expected={'matrix':old};chain.revision='1'
        chain.ledger=Mock(side_effect=[before,before,after,after,after]);chain.save_ledger=Mock();chain.install_fault=Mock();chain.remove_fault=Mock();chain.read=Mock();chain.marker_case=Mock()
        chain.call=Mock(side_effect=[({},{}),({},{}),({'ok':True},{'X-Role-Matrix-Revision':'2','Cache-Control':'private, no-store'})]);chain.exercise(before)
        calls=chain.call.call_args_list;self.assertEqual(len(calls),3);first,last=calls[0],calls[2]
        self.assertIs(first.args[3],last.args[3]);self.assertEqual(first.kwargs['headers'],{'If-Role-Matrix-Revision':'1'});self.assertEqual(first.kwargs['headers'],last.kwargs['headers']);self.assertEqual([first.kwargs['index'],last.kwargs['index']],[0,1]);self.assertEqual(first.kwargs['code'],'admin_unavailable');self.assertEqual(chain.read.call_args.kwargs['user'],'target');self.assertEqual(chain.read.call_count,5)
    def test_first_http_error_and_restore_error_preserve_primary(self):
        chain=self.chain();chain.payload={};chain.revision='1';chain.install_fault=Mock();chain.call=Mock(side_effect=AssertionError('HTTP first'));chain.remove_fault=Mock(side_effect=RuntimeError('restore second'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with self.assertRaisesRegex(AssertionError,'HTTP first'):chain.exercise({})
        chain.call.assert_called_once();chain.remove_fault.assert_called_once()
    def test_read_revision_cache_or_DTO_mismatch_rejected(self):
        chain=self.chain()
        for headers in ({'X-Role-Matrix-Revision':'2','Cache-Control':'private, no-store'},{'X-Role-Matrix-Revision':'1','Cache-Control':'public'}):
            chain.call=Mock(return_value=({},headers))
            with self.assertRaises(AssertionError):chain.read({},'1',1)
        chain.call=Mock(side_effect=lambda *a,**k:k['validate']({'wrong':'DTO'}))
        with self.assertRaises(AssertionError):chain.read({},'1',1)
    def test_raw_owned_receipt_no_cookie_with_exact_revision(self):
        chain=self.chain();payload={'matrix':[]}
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[1],'PUT',chain.path,json.dumps(payload).encode(),{'If-Role-Matrix-Revision':'1','Cookie':'SECRET'})
        with patch.object(c.base.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(503,b'{"error":{"code":"admin_unavailable"}}',{'Set-Cookie':'SECRET'})):
            chain.call('admin','PUT',chain.path,payload)
        raw=(self.work/'role-wire-1.json').read_text();self.assertNotIn('SECRET',raw);v=json.loads(raw);self.assertEqual(v['If_Role_Matrix_Revision'],'1');self.assertEqual(len(v['response_sha256']),64);self.assertEqual(v['API_index'],1);self.assertEqual((self.work/'role-wire-1.json').stat().st_mode&0o777,0o600)
    def test_raw_IO_error_restores_private_transport(self):
        chain=self.chain()
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[0],'GET',chain.path)
        with patch.object(c.base.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(200,b'{}',{})) as wire,patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.call('admin','GET',chain.path)
            self.assertIs(c.base.transport,wire)
    def test_ledger_full_rows_scope_and_all_other_public_hashes_readonly(self):
        chain=self.chain();chain.sql.side_effect=['["sessions","rbac_user_roles"]','{}'];chain.ledger();query=chain.sql.call_args.args[0]
        for text in ('to_jsonb(t)','rbac_roles','v3_role_grants','management_audit','rbac_user_roles','sessions','json_object_agg'):self.assertIn(text,query)
        self.assertTrue(chain.sql.call_args.kwargs['read'])
    def test_private_helper_ports_and_contract_scope(self):
        self.assertIsNot(c.base,shared);self.assertEqual(shared.PORTS,(18606,18607));self.assertEqual(c.base.PORTS,(18852,18853))
        contract=c.Contract(c.ROOT)
        for method,path in [('GET','/api/v1/roles/owned'),('PUT','/api/v1/roles/owned'),('POST','/api/v1/auth/login')]:contract.canonical(method,path)
        for method,path in [('PUT','/api/v1/workspace'),('GET','/api/v1/roles/owned?x=1'),('GET','https://foreign/api/v1/roles/owned')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
    def test_source_drift_stops_before_wire_and_evidence_is_not_registration(self):
        chain=self.chain();chain.frozen.side_effect=c.Blocked('source drift')
        with patch.object(c.base.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):chain.call('admin','GET',chain.path)
        wire.assert_not_called();v=c.evidence({'registration_evidence':True});self.assertIs(v['registration_evidence'],False);self.assertEqual(v['binary_sha256'],c.BINARY)
    def test_inventory_and_missing_live_never_acquire(self):
        with patch.object(c,'doctor') as doctor,patch.object(c.os,'open') as opened,patch.object(c.sqlite3,'connect') as db,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(c.main(['--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['estimated_HTTP_calls'],14);doctor.assert_not_called();opened.assert_not_called();db.assert_not_called()
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
    def test_exact_source_contract_audit_and_grouped_identity(self):
        src=(c.ROOT/'internal/v3admin/role_write.go').read_text();catalog=(c.ROOT/'internal/v3admin/catalog.go').read_text();handler=(c.ROOT/'internal/v3admin/service.go').read_text()
        for token in ('If-Role-Matrix-Revision','X-Role-Matrix-Revision','v3.role.matrix.updated','tx.Commit(ctx)','v3_matrix_revision=v3_matrix_revision+1'):self.assertIn(token,src)
        self.assertIn('"identity", "成员与角色", "Members & roles", []string{"members", "roles"}',catalog);self.assertIn('"admin_unavailable"',handler)
        self.assertEqual(tuple(__import__('re').findall(r'^\s*\{"([a-z_]+)",',catalog,__import__('re').M)),c.CODES)
    def test_transitive_hashes_and_prepared_AST(self):
        hashes=c.hashes()
        for n in ('identity.py','inventory.py','tests/test_api_role_audit_rollback_coverage.py','runner.py'):self.assertEqual(len(hashes[n]),64)
        ast.parse(Path(c.__file__).read_text());self.assertEqual(len(c.REQUIRED_CASES),11)
    def test_exact_PG_resource_limits_never_allow_foreign_or_capacity_drift(self):
        good={'Id':c.PG_CID,'HostConfig':{'NanoCpus':4_000_000_000,'Memory':2_147_483_648}}
        with patch.object(c,'_inspect',return_value=good):self.assertEqual(c.base.inspect_pg('owned'),good)
        for info in ({'Id':c.PG_CID},dict(good,HostConfig={'NanoCpus':2_000_000_000,'Memory':2_147_483_648}),dict(good,HostConfig={'NanoCpus':4_000_000_000,'Memory':1_073_741_824})):
            with patch.object(c,'_inspect',return_value=info):
                with self.assertRaises(AssertionError):c.base.inspect_pg('owned')
    def test_baseline_rejects_target_assignment_to_admin_before_matrix_read(self):
        chain=self.chain();chain.admin_email='synthetic@testlab.invalid';chain.users={'target':'c'*32};chain.sql.side_effect=['b'*32,json.dumps(['b'*32])];chain.call=Mock()
        with self.assertRaises(AssertionError):chain.baseline()
        chain.call.assert_not_called()
    def test_failed_account_permission_does_not_skip_fault_removal(self):
        chain=self.chain();before,_,old,new=self.data();chain.payload={'matrix':new};chain.expected={'matrix':old};chain.revision='1'
        chain.install_fault=Mock();chain.remove_fault=Mock();chain.ledger=Mock(return_value=before);chain.save_ledger=Mock();chain.marker_case=Mock();chain.read=Mock()
        chain.call=Mock(side_effect=[({},{}),AssertionError('target wrongly allowed')])
        with self.assertRaisesRegex(AssertionError,'target wrongly allowed'):chain.exercise(before)
        chain.remove_fault.assert_called_once();self.assertEqual(chain.call.call_count,2)
if __name__=='__main__':unittest.main()

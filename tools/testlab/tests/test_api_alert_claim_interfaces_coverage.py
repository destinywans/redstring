"""Pure offline alert DTO/CAS/authority/fault/lifecycle counterexamples."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_alert_claim_interfaces_coverage as c
import api_management_chain as shared
class AlertClaimTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        chain=c.Chain.__new__(c.Chain);chain.work=self.work;chain.nonce='a'*32;chain.app='app-owned';chain.fault_target='second';chain.first='first';chain.hidden_alert='hidden'
        chain.source_id='core-'+chain.nonce;chain.users={'reader':'b'*32,'peer':'c'*32};chain.fault_attempted=False;chain.raw_number=0;chain.generation=0
        chain.database='aiops_testlab_management_'+chain.nonce;chain.db_user='management_'+chain.nonce;chain.owner_tag='owner:uuid'
        chain.frozen=Mock();chain.owner=Mock();chain.sql=Mock()
        return chain
    def row(self,id='first',env='test'):
        return dict(id=id,source_id='core-'+'a'*32,application_id='app-owned',environment_id='native-'+env,fingerprint=id,labels={'fixture':'owned'},title='Owned alert',severity='critical',
            status='open',alert_status='firing',summary='Owned source summary',assignee_id='',investigation_id='',starts_at='2026-10-06T00:00:00+00:00',updated_at='2026-10-06T00:00:00+00:00',revision='100',other='preserved')
    def data(self,target='first'):
        before=dict(incidents=[self.row('first'),self.row('second'),self.row('hidden','staging')],business={'sessions':'digest','runbooks':'digest'},audits=[dict(id=1,old=True)])
        after=copy.deepcopy(before);new=next(r for r in after['incidents'] if r['id']==target);new.update(assignee_id='b'*32,updated_at='2026-10-06T00:00:01+00:00',revision='147')
        after['audits'].append(dict(id=3,actor_id='b'*32,actor_kind='user',application_id='app-owned',action='alert.claim',target=target,outcome='succeeded',changes={'environment_id':'test','assignee_id':'b'*32}))
        return before,after,new
    def dto(self,row):
        v={k:None for k in c.DETAIL_FIELDS};v.update(id=row['id'],revision=row['revision'],severity='P1',title=row['title'],state=row['alert_status'],source_id=row['source_id'],env='test',since=row['starts_at'],
            summary=row['summary'],assignee_id=row['assignee_id'],investigation_id='',notifications=[],notifications_truncated=False,grouping={'group':None,'inhibited':None,'incident':{'id':row['id']},'change':None},unavailable=list(c.UNAVAILABLE));return v
    def test_revision_positive_uint32_not_lease_etag_or_plusone(self):
        for value in ('1','4294967295'):self.assertTrue(c.valid_revision(value))
        for value in ('','0','01','4294967296',' 1','-1','W/1','"1"','1,2',1,None):self.assertFalse(c.valid_revision(value))
        before,after,_=self.data();self.assertNotEqual(int(after['incidents'][0]['revision']),int(before['incidents'][0]['revision'])+1)
        c.verify_claim(before,after,'first','b'*32,'app-owned')
    def test_detail_exact_persisted_fields_and_unknown_providers(self):
        row=self.row();value=self.dto(row);c.verify_detail(value,row,row['source_id'],'test')
        for patch in ({'revision':'101'},{'source_id':'foreign'},{'env':'staging'},{'assignee_id':'other'},{'series':[]},{'labels':[]},{'notifications_truncated':True},{'unavailable':[]}):
            with self.assertRaises(AssertionError):c.verify_detail(dict(value,**patch),row,row['source_id'],'test')
        with self.assertRaises(AssertionError):c.verify_detail(dict(value,lease_expires_at='made-up'),row,row['source_id'],'test')
    def test_detail_native_since_and_incident_association_not_guessed(self):
        row=self.row();value=self.dto(row)
        for patch in ({'since':'2026-10-06T00:00:01Z'},{'grouping':{'incident':{'id':'uncreated-case'}}}):
            with self.assertRaises(AssertionError):c.verify_detail(dict(value,**patch),row,row['source_id'],'test')
    def test_claim_only_one_row_assignee_timestamp_revision_and_one_audit(self):
        before,after,new=self.data();self.assertEqual(c.verify_claim(before,after,'first','b'*32,'app-owned'),new)
        for key,value in [('revision','100'),('assignee_id','other'),('updated_at',before['incidents'][0]['updated_at']),('source_id','foreign'),('labels',{}),('alert_status','resolved')]:
            bad=copy.deepcopy(after);bad['incidents'][0][key]=value
            with self.assertRaises(AssertionError):c.verify_claim(before,bad,'first','b'*32,'app-owned')
        bad=copy.deepcopy(after);bad['incidents'][1]['assignee_id']='b'*32
        with self.assertRaises(AssertionError):c.verify_claim(before,bad,'first','b'*32,'app-owned')
    def test_full_business_and_audit_prefix_no_hidden_residue(self):
        before,after,_=self.data()
        for key in after['business']:
            bad=copy.deepcopy(after);bad['business'][key]='changed'
            with self.assertRaises(AssertionError):c.verify_claim(before,bad,'first','b'*32,'app-owned')
        for rows in (after['audits'][1:],after['audits'][:-1],after['audits']+[dict(after['audits'][-1],id=4)]):
            with self.assertRaises(AssertionError):c.verify_claim(before,dict(after,audits=rows),'first','b'*32,'app-owned')
    def test_counterexample_wrong_success_outcome_then_source_succeeded(self):
        before,after,_=self.data();bad=copy.deepcopy(after);bad['audits'][-1]['outcome']='success'
        with self.assertRaises(AssertionError):c.verify_claim(before,bad,'first','b'*32,'app-owned')
        c.verify_claim(before,after,'first','b'*32,'app-owned')
        self.assertIn("'succeeded'",(c.ROOT/'internal/v3events/claim.go').read_text())
    def test_audit_actor_app_target_and_exact_redacted_changes(self):
        before,after,_=self.data()
        for key,value in [('actor_id','other'),('application_id','foreign'),('target','second'),('changes',{'environment_id':'test','assignee_id':'other'}),('action','other')]:
            bad=copy.deepcopy(after);bad['audits'][-1][key]=value
            with self.assertRaises(AssertionError):c.verify_claim(before,bad,'first','b'*32,'app-owned')
    def test_refusal_retry_and_rollback_compare_complete_rows(self):
        before,after,_=self.data();c.verify_unchanged(before,copy.deepcopy(before))
        for key in ('incidents','audits','business'):
            bad=copy.deepcopy(before);bad[key]=after[key] if after[key]!=before[key] else {'unexpected':'residue'}
            with self.assertRaises(AssertionError):c.verify_unchanged(before,bad)
    def test_fault_exact_actor_app_secondtarget_succeeded_scope(self):
        _,create,drop=c.fault_statements('a'*32,'b'*32,'app-owned','second')
        for v in ("NEW.actor_id='"+'b'*32+"'","NEW.application_id='app-owned'","NEW.target='second'","NEW.action='alert.claim'","NEW.outcome='succeeded'","jsonb_build_object('environment_id','test','assignee_id'"):
            self.assertIn(v,create)
        self.assertNotIn("NEW.outcome='success'",create);self.assertIn('DROP TRIGGER IF EXISTS',drop)
        with self.assertRaises(AssertionError):c.fault_statements('a'*32,'b'*32,'app',"bad';drop")
    def test_unknown_ddl_and_evidencefailure_keep_restore_intent(self):
        chain=self.chain();chain.sql.side_effect=RuntimeError('unknown commit')
        with self.assertRaises(RuntimeError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual((self.work/'claim-fault-intent.json').stat().st_mode&0o777,0o600)
        chain=self.chain()
        with patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.install_fault()
        self.assertTrue(chain.fault_attempted);chain.sql.assert_not_called()
    def test_restore_owner_drop_readback_all_use_same4deadline(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':False,'function':False})])
        with patch.object(c.time,'monotonic',return_value=100):chain.remove_fault(deadline=102)
        chain.owner.assert_called_once_with(deadline=102);self.assertEqual([r.kwargs['deadline'] for r in chain.pg.call_args_list],[102,102]);self.assertFalse(chain.fault_attempted);chain.frozen.assert_not_called()
    def test_remaining_trigger_keeps_intent_without_ddl_retry(self):
        chain=self.chain();chain.fault_attempted=True;chain.pg=Mock(side_effect=['',json.dumps({'trigger':True,'function':False})])
        with self.assertRaises(AssertionError):chain.remove_fault()
        self.assertTrue(chain.fault_attempted);self.assertEqual(chain.pg.call_count,2)
    def test_cleanup_restore_and_original_share_one40budget(self):
        chain=self.chain();chain.remove_fault=Mock()
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;chain.cleanup()
        chain.remove_fault.assert_called_once_with(deadline=104);self.assertEqual(chain.cleanup_deadline,140);cleanup.assert_called_once_with(chain)
    def test_restore_failure_still_cleanup_never_green_or_overwrite_first(self):
        chain=self.chain();chain.remove_fault=Mock(side_effect=RuntimeError('restore first'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with patch.object(c,'bounded_cleanup',side_effect=AssertionError('PG second')) as cleanup,patch.object(c,'emit') as emit:
            with self.assertRaisesRegex(RuntimeError,'restore first'):chain.cleanup()
        cleanup.assert_called_once_with(chain);emit.assert_not_called()
    def test_first_http_red_not_overwritten_by_finally_no_auto_recovery(self):
        chain=self.chain();before,_,_=self.data('second');chain.ledger=Mock(return_value=before);chain.save_ledger=Mock();chain.install_fault=Mock()
        chain.claim=Mock(side_effect=AssertionError('HTTP first'));chain.remove_fault=Mock(side_effect=RuntimeError('restore second'));chain.diagnostic=Mock(side_effect=OSError('full'))
        with self.assertRaisesRegex(AssertionError,'HTTP first'):chain.audit_recovery()
        chain.claim.assert_called_once();self.assertEqual(chain.claim.call_args.kwargs['code'],'events_unavailable')
    def test_audit503_then_same_prior_revision_otherAPI_recovery(self):
        chain=self.chain();before,after,new=self.data('second');chain.ledger=Mock(side_effect=[before,before,before,after]);chain.save_ledger=Mock();chain.install_fault=Mock();chain.remove_fault=Mock();chain.marker_case=Mock()
        dto=self.dto(new);chain.detail=Mock(side_effect=[None,dto,dto]);chain.claim=Mock(side_effect=[{},dto]);chain.audit_recovery()
        first,last=chain.claim.call_args_list;self.assertEqual(first.args,last.args);self.assertEqual(first.kwargs['want'],503);self.assertEqual(last.kwargs['index'],1);self.assertEqual(chain.claim.call_count,2)
    def test_same_actor_new_revision_retry_old412_and_other_current409(self):
        chain=self.chain();before,after,new=self.data();dto=self.dto(new);chain.ledger=Mock(side_effect=[before,after,after]);chain.claim=Mock(side_effect=[dto,dto]);chain.save_ledger=Mock();chain.marker_case=Mock();chain.reject=Mock();chain.detail=Mock(return_value=dto)
        chain.normal_and_CAS();self.assertEqual(chain.claim.call_args_list[1].args[1],new['revision']);self.assertEqual(chain.claim.call_args_list[1].kwargs['index'],1)
        old,other=chain.reject.call_args_list;self.assertEqual(old.args[4:6],(412,'claim_revision_changed'));self.assertEqual(other.args[4:6],(409,'already_claimed_or_closed'))
    def test_independent_view_change_gates_use_exact_synthetic_grants(self):
        chain=self.chain();chain.claim=Mock();chain.reject=Mock();chain.detail=Mock();chain.grant=Mock();chain.marker_case=Mock();chain.authorization('100')
        self.assertEqual(chain.grant.call_args_list[0].args,('reader','change',False));self.assertEqual(chain.grant.call_args_list[2].args,('reader','view',False))
        self.assertTrue(any(r.args[:2]==('denied','GET') and r.args[4]==403 for r in chain.reject.call_args_list))
        self.assertTrue(any(r.args[:2]==('hidden','POST') and r.args[4]==404 and r.kwargs['environment']=='staging' for r in chain.reject.call_args_list))
    def test_seed_does_not_inherit_broad_events_environment_view(self):
        chain=self.chain()
        with patch.object(c.base.Chain,'seed') as seed,patch.object(c.core.Chain,'seed') as core:chain.seed()
        seed.assert_called_once_with(chain);core.assert_not_called()
    def test_grant_exact_application_role_and_no_business_write(self):
        chain=self.chain();chain.roles={'reader':'own-role'};chain.grant('reader','change',True);create=chain.sql.call_args.args[0]
        self.assertIn("'events','change','application','app-owned'",create);self.assertNotIn('ops_incidents',create)
        chain.grant('reader','change',False);self.assertIn("role_id='own-role'",chain.sql.call_args.args[0])
        with self.assertRaises(AssertionError):chain.grant('admin','change',True)
    def test_two_generations_emit_four_unique_immutable_ready_names(self):
        chain=self.chain();seen=[]
        def start(obj):
            for index in (0,1):c.base.emit('management.api_ready.'+str(index),'passed',{'synthetic':True})
        with patch.object(c.base.Chain,'start',side_effect=start),patch.object(c.base,'emit',side_effect=lambda name,*args,**kwargs:seen.append(name)) as original:
            chain.start();chain.start();self.assertIs(c.base.emit,original)
            with self.assertRaises(AssertionError):chain.start()
        self.assertEqual(tuple(seen),c.READY_CASES);self.assertEqual(len(seen),len(set(seen)))
    def test_private_core_and_passed_management_globals_unchanged(self):
        self.assertIs(c.core.management,c.base);self.assertIsNot(c.base,shared);self.assertEqual(shared.PORTS,(18606,18607));self.assertEqual(c.base.PORTS,(18840,18841))
        self.assertIs(c.core.Chain.services_and_ingress.__globals__['management'],c.base)
    def test_private_evidence_keeps_alert_scope_not_workspace_decorator(self):
        path=self.work/'interfaces.jsonl';c.append_json(path,{'method':'POST','path':'/api/v1/alerts/{id}/claim'})
        row=json.loads(path.read_text());self.assertIn('claim audit rollback/recovery',row['proof_scope']);self.assertNotIn('workspace audit fault',row['proof_scope'])
        self.assertFalse(row['registration_evidence']);self.assertEqual(row['binary_sha256'],c.BINARY)
    def test_private_ingress_no_sql_business_seed_and_notifications_disabled(self):
        source=c._core_source.split('    def services_and_ingress(self):',1)[1].split('\n    def events(self)',1)[0]
        self.assertNotIn('INSERT INTO',source);self.assertIn("'/api/v1/services'",source);self.assertIn("'/api/v1/webhooks/alertmanager/'",source)
        self.assertIn("'feishu_channels':[]",source);self.assertIn("'notification_delivery':'not_configured'",source)
    def test_raw_receipt_excludes_cookie_and_bearer_and_restores_transport(self):
        chain=self.chain();raw=b'{"expected_revision":"100"}'
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[1],'POST','/api/v1/alerts/first/claim',b'{}',{'Cookie':'do-not-save','Authorization':'Bearer do-not-save'},12)
        with patch.object(c.core.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(503,b'{"error":{"code":"events_unavailable"}}',{'Set-Cookie':'do-not-save'})) as transport:
            chain.call('reader','POST','/api/v1/alerts/first/claim',{},raw_body=raw);self.assertIs(c.base.transport,transport)
        text=(self.work/'alert-wire-1.json').read_text();self.assertNotIn('do-not-save',text);value=json.loads(text)
        self.assertEqual(value['request_body'],raw.decode());self.assertEqual(value['API_index'],1);self.assertEqual(len(value['response_sha256']),64)
    def test_raw_io_failure_restores_transport_without_retry(self):
        chain=self.chain()
        def call(*args,**kwargs):return c.base.transport(c.base.BASES[0],'GET','/api/v1/alerts/first')
        with patch.object(c.core.Chain,'call',side_effect=call),patch.object(c.base,'transport',return_value=(200,b'{}',{})) as transport,patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):chain.call('reader','GET','/api/v1/alerts/first')
            self.assertIs(c.base.transport,transport);self.assertEqual(transport.call_count,1)
    def test_malformed_fixture_cannot_escape_claim_inventory(self):
        chain=self.chain()
        with patch.object(c.core.Chain,'call') as wire:
            with self.assertRaises(AssertionError):chain.call('reader','GET','/api/v1/alerts/first',raw_body=b'{')
        wire.assert_not_called()
    def test_public_logout_old_session_twoAPIs_not_natural_expiry_claim(self):
        chain=self.chain();before,_,_=self.data();chain.ledger=Mock(return_value=before);chain.call=Mock();chain.reject=Mock();chain.detail=Mock();chain.marker_case=Mock()
        chain.session_revocation();self.assertEqual(chain.call.call_args.args[:3],('reader','POST','/api/v1/auth/logout'));self.assertEqual(chain.call.call_args.kwargs['want'],204)
        self.assertEqual(chain.reject.call_count,4);self.assertEqual({r.kwargs['index'] for r in chain.reject.call_args_list},{0,1});self.assertTrue(all(r.args[4]==401 for r in chain.reject.call_args_list))
    def test_full_ledger_keeps_xmin_rows_and_other_tables_readonly(self):
        chain=self.chain();chain.sql.side_effect=['["sessions","ops_notifications"]','{}'];chain.ledger();query=chain.sql.call_args.args[0]
        self.assertIn("jsonb_build_object('revision',xmin::text)",query);self.assertIn('json_object_agg',query);self.assertIn('ops_notifications',query);self.assertTrue(chain.sql.call_args.kwargs['read'])
    def test_source_drift_stops_next_wire(self):
        chain=self.chain();chain.frozen.side_effect=c.Blocked('source drift')
        with patch.object(c.core.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):chain.call('reader','GET','/api/v1/alerts/first')
        wire.assert_not_called()
    def test_inventory_and_no_optin_acquire_nothing_normal_added_zero(self):
        with patch.object(c.support,'doctor') as doctor,patch.object(c.os,'open') as opened,patch.object(c.sqlite3,'connect') as db,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(c.main(['--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['normal_added'],0);doctor.assert_not_called();opened.assert_not_called();db.assert_not_called()
        with patch.object(c,'admission') as admit,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admit.assert_not_called()
    def test_contract_only_claim_detail_and_precise_support_no_investigation(self):
        contract=c.Contract(c.ROOT)
        for method,path in (*c.TARGETS,*c.SUPPORT):contract.canonical(method,'/api/v1'+path.replace('{id}','first').replace('{source}','own'))
        for method,path in [('POST','/api/v1/incidents'),('GET','/api/v1/alerts/first?lease=1'),('POST','https://external/api/v1/alerts/first/claim')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
    def permit(self):
        run=str(uuid.uuid4());p=dict(status='root-admitted-alert-claim-interfaces',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},manifest_sha256='c'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,alert_claim_audit_fault_authorized=True)
        row=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='c'*64,pid=os.getppid());return p,row
    def admit(self,p,rows):
        path=c.ROOT/'.local/testlab'/('offline-'+p['resource_run_id']+'.json')
        with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=os.getuid()))()),patch.object(c.base,'private_json',return_value=p),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as db:
            db.return_value.execute.return_value=[(json.dumps(r),) for r in rows];return c.admission(path,p['resource_run_id'])
    def test_sole_matching_runner_manifest_source_parent(self):
        p,row=self.permit();self.admit(p,[row])
        for rows in ([],[row,row],[dict(row,pid=os.getppid()+1)],[dict(row,selected_suite='other')],[dict(row,source_tree_sha='d'*64)],[dict(row,manifest_sha='d'*64)]):
            with self.assertRaises(AssertionError):self.admit(p,rows)
    def test_exact_root_identity_permission_transitivecomponents_hash(self):
        p,row=self.permit()
        for key in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','fresh_nonce_hmac_process_tuple_verified','headroom_verified','alert_claim_audit_fault_authorized'):
            bad=copy.deepcopy(p);bad[key]=False
            with self.assertRaises(AssertionError):self.admit(bad,[row])
        self.assertIn('components.py',c.hashes())
if __name__=='__main__':unittest.main()

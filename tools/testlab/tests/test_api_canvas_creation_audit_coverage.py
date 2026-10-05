"""Offline contract, partial-row residue, fault recovery and root admission guards."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_canvas_creation_audit_coverage as c
import api_management_chain as shared
class CanvasCreationTests(unittest.TestCase):
    def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.actor='b'*32;x.app='app-owned';x.envrow='env-owned';x.users={'reader':x.actor};x.roles={'reader':'own-role'};x.marker='owned';x.owner_name='owned reader';x.raw_number=0;x.fault_attempted=False;x.fault_payload=None;x.database='database-own';x.db_user='role-own';x.owner_tag='owner:uuid';x.frozen=Mock();x.owner=Mock();x.sql=Mock();return x
    def data(self,kind):
        x=self.chain();p=x.payload(kind);before={t:[] for t in c.TABLES};before['business']={'sessions':'stable','runs':'empty'};after=copy.deepcopy(before);id=('CV-' if kind=='plan' else 'INV-')+'c'*32;stamp='2026-10-06T00:00:00+00:00'
        after['management_audit']=[dict(id=8,actor_id=x.actor,actor_kind='user',application_id='' if kind=='plan' else x.app,action=c.ACTIONS[kind],target=id,outcome='success',changes=c.changes(p),created_at=stamp)]
        if kind=='plan':after['v3_canvases']=[dict(id=id,owner_id=x.actor,environment_id='test',title=p['title'],kind=kind,goal=p['goal'],services=[x.app],status='draft',nodes=[c.root_node(p['title'])],edges=[],artifacts=[],created_at=stamp,updated_at=stamp)]
        else:
            branch='d'*32;after['investigations']=[dict(id=id,application_id=x.app,environment_id=x.envrow,created_by=x.actor,title=p['title'],goal=p['goal'],status='open',created_at=stamp,updated_at=stamp)]
            after['branches']=[dict(id=branch,investigation_id=id,parent_id=None,title='主调查',model_id='',revision=0,inherited_parent_revision=None,synced_parent_revision=None)]
            after['context_revisions']=[dict(branch_id=branch,revision=0,messages=[],provenance={'kind':'initial'},created_at=stamp)]
            after['events']=[dict(seq=5,investigation_id=id,branch_id=branch,type='investigation.created',payload={'id':id},created_at=stamp)]
        return x,p,before,after,id
    def verify(self,kind='plan'):
        x,p,b,a,id=self.data(kind);return x,p,b,a,id,c.verify_creation(b,a,p,x.actor,x.app,x.envrow,id)
    def graph(self,id,p,rows,x):
        return dict(investigation=dict(id=id,title=p['title'],status='active',env='test',participants=[x.owner_name]),loop=[],tray=[],nodes=[dict(id=rows['branches'][0]['id'],kind='root',title='主调查',subtitle='Main investigation',status='active',x=16,y=40,w=260,collapsed=False,layout_version=0,context={'version':'v0','tokens':None,'messages':0,'excludes':[]},versions=[{'v':'v0','text':'Context revision 0','at':rows['context_revisions'][0]['created_at']}])])
    def test_plan_exact_native_blank_creation(self):self.verify('plan')
    def test_investigation_exact_blank_root_context_event(self):self.verify('investigation')
    def test_counterexample_investigation_partial_rows_rejected(self):
        x,p,b,a,id=self.data('investigation')
        for t in ('investigations','branches','context_revisions','events','management_audit'):
            bad=copy.deepcopy(a);bad[t]=[]
            with self.assertRaises(AssertionError):c.verify_creation(b,bad,p,x.actor,x.app,x.envrow,id)
        c.verify_creation(b,a,p,x.actor,x.app,x.envrow,id)
    def test_no_prior_row_change_delete_or_duplicate_after_random_id_sort(self):
        old=[{'id':'z','all':'kept'}];new=[{'id':'a','all':'new'},old[0]];self.assertEqual(c.additions(old,new,1),[new[0]])
        for bad in ([],[dict(old[0],all='changed')],[old[0],old[0]]):
            with self.assertRaises(AssertionError):c.additions(old,bad,0)
    def test_audit_identity_changes_prefix_and_extra_write_rejected(self):
        for kind in c.ACTIONS:
            x,p,b,a,id=self.data(kind)
            for key,value in [('actor_id','foreign'),('application_id','wrong'),('target','wrong'),('outcome','succeeded'),('changes',{})]:
                bad=copy.deepcopy(a);bad['management_audit'][0][key]=value
                with self.assertRaises(AssertionError):c.verify_creation(b,bad,p,x.actor,x.app,x.envrow,id)
            bad=copy.deepcopy(a);bad['business']['runs']='one'
            with self.assertRaises(AssertionError):c.verify_creation(b,bad,p,x.actor,x.app,x.envrow,id)
    def test_root_owner_binding_and_no_ai_history_residue(self):
        for kind in c.ACTIONS:
            x,p,b,a,id=self.data(kind);table='v3_canvases' if kind=='plan' else 'investigations'
            for key,value in [('environment_id','foreign'),('title','other'),('status','closed')]:
                bad=copy.deepcopy(a);bad[table][0][key]=value
                with self.assertRaises(AssertionError):c.verify_creation(b,bad,p,x.actor,x.app,x.envrow,id)
        x,p,b,a,id=self.data('investigation')
        for table,key,value in [('branches','parent_id','foreign'),('branches','model_id','made-up'),('branches','revision',1),('context_revisions','messages',[{'role':'assistant'}]),('context_revisions','provenance',{'kind':'ai_draft'}),('events','branch_id','foreign')]:
            bad=copy.deepcopy(a);bad[table][0][key]=value
            with self.assertRaises(AssertionError):c.verify_creation(b,bad,p,x.actor,x.app,x.envrow,id)
    def test_plan_strict_secondAPI_detail_threads(self):
        x,p,b,a,id,rows=self.verify();v=dict(id=id,title=p['title'],kind='plan',status='draft',participants=[x.owner_name],note=p['goal'],nodes=[c.root_node(p['title'])],edges=[],artifacts=[],threads={'root':{'context':'主线','turns':[]}},env='test');c.verify_readback(v,rows,p,id,x.owner_name)
        for key,value in [('threads',{}),('participants',['other']),('nodes',[])]:
            with self.assertRaises(AssertionError):c.verify_readback(dict(v,**{key:value}),rows,p,id,x.owner_name)
    def test_graph_projection_y40_not_generic_y140_and_native_timestamp(self):
        x,p,b,a,id,rows=self.verify('investigation');v=self.graph(id,p,rows,x);c.verify_readback(v,rows,p,id,x.owner_name)
        for key,value in [('y',140),('parent','invented'),('context',{'messages':1})]:
            bad=copy.deepcopy(v);bad['nodes'][0][key]=value
            with self.assertRaises(AssertionError):c.verify_readback(bad,rows,p,id,x.owner_name)
        bad=copy.deepcopy(v);bad['nodes'][0]['versions'][0]['at']='2026-10-06T00:00:01Z'
        with self.assertRaises(AssertionError):c.verify_readback(bad,rows,p,id,x.owner_name)
    def test_fault_exact_success_two_actions_title_service_lineage(self):
        for kind in c.ACTIONS:
            x,p,_,_,_=self.data(kind);name,create,drop=c.fault_statements(x.nonce,x.actor,x.app,x.envrow,p)
            for value in (c.ACTIONS[kind],"NEW.outcome='success'",'NEW.changes=',p['title'],x.app,"NEW.actor_kind='user'",x.actor):self.assertIn(value,create)
            self.assertIn('EXISTS(SELECT 1 FROM',create);self.assertIn(name,drop);self.assertIn('DROP TRIGGER IF EXISTS',drop)
            self.assertIn('v3_canvases c' if kind=='plan' else 'investigations i',create)
            for bad in (dict(p,ai_draft=True),dict(p,services=['foreign']),dict(p,template_id='fake')):
                with self.assertRaises(AssertionError):c.fault_statements(x.nonce,x.actor,x.app,x.envrow,bad)
    def test_unknown_ddl_and_private_io_failure_keep_exact_restore_intent(self):
        x=self.chain();p=x.payload('plan');x.sql.side_effect=RuntimeError('unknownDDL')
        with self.assertRaises(RuntimeError):x.install_fault(p)
        self.assertTrue(x.fault_attempted);self.assertEqual(x.fault_payload,p);self.assertEqual((x.work/'canvas-fault-intent.json').stat().st_mode&0o777,0o600)
        x=self.chain()
        with patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):x.install_fault(p)
        self.assertTrue(x.fault_attempted);x.sql.assert_not_called()
    def test_restore_owner_drop_absence_share_four_seconds_no_source_guard(self):
        x=self.chain();x.fault_attempted=True;x.fault_payload=x.payload('plan');x.pg=Mock(side_effect=['',json.dumps({'trigger':False,'function':False})])
        with patch.object(c.time,'monotonic',return_value=100):x.remove_fault(deadline=102)
        x.owner.assert_called_once_with(deadline=102);self.assertEqual([r.kwargs['deadline'] for r in x.pg.call_args_list],[102,102]);x.frozen.assert_not_called();self.assertFalse(x.fault_attempted)
    def test_restore_remaining_fault_stays_blocked(self):
        x=self.chain();x.fault_attempted=True;x.fault_payload=x.payload('investigation');x.pg=Mock(side_effect=['',json.dumps({'trigger':True,'function':False})])
        with self.assertRaises(AssertionError):x.remove_fault()
        self.assertTrue(x.fault_attempted);self.assertEqual(x.pg.call_count,2)
    def test_cleanup_single40_with_restore4_then_original(self):
        x=self.chain();x.remove_fault=Mock()
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
        x.remove_fault.assert_called_once_with(deadline=104);self.assertEqual(x.cleanup_deadline,140);cleanup.assert_called_once_with(x)
    def test_restore_failure_still_original_cleanup_first_failure_preserved(self):
        x=self.chain();x.remove_fault=Mock(side_effect=RuntimeError('restore first'));x.diagnostic=Mock(side_effect=OSError('full'))
        with patch.object(c,'bounded_cleanup',side_effect=AssertionError('PG second')) as cleanup,patch.object(c,'emit') as emit:
            with self.assertRaisesRegex(RuntimeError,'restore first'):x.cleanup()
        cleanup.assert_called_once_with(x);emit.assert_not_called()
    def test_first_http_red_not_replaced_by_finally_or_auto_retry(self):
        x,p,b,a,id=self.data('plan');x.ledger=Mock(return_value=b);x.save_ledger=Mock();x.install_fault=Mock();x.call=Mock(side_effect=AssertionError('HTTP first'));x.remove_fault=Mock(side_effect=RuntimeError('restore second'));x.diagnostic=Mock(side_effect=OSError('full'))
        with self.assertRaisesRegex(AssertionError,'HTTP first'):x.exercise('plan')
        x.call.assert_called_once();self.assertEqual(x.call.call_args.kwargs['code'],'audit_unavailable')
    def test_full_rollback_guard_for_each_table_and_digest(self):
        _,_,b,a,_=self.data('investigation');c.same(b,copy.deepcopy(b))
        for key in b:
            bad=copy.deepcopy(b);bad[key]=(a[key] if a[key]!=b[key] else [{'owned':'unexpected residue'}]) if key!='business' else {'changed':'yes'}
            with self.assertRaises(AssertionError):c.same(b,bad)
    def test_recovery_samebody_otherAPI_one_success_not_idempotency(self):
        for kind in c.ACTIONS:
            x,p,b,a,id=self.data(kind);x.ledger=Mock(side_effect=[b,b,a,a]);x.save_ledger=Mock();x.install_fault=Mock();x.remove_fault=Mock();x.marker_case=Mock();x.call=Mock(side_effect=[({},{}),({'id':id,'href':'/canvas/'+id},{}),({}, {})])
            with patch.object(c,'verify_readback'):x.exercise(kind)
            first,second,read=x.call.call_args_list;self.assertEqual(first.args,second.args);self.assertEqual(first.kwargs['want'],503);self.assertEqual(second.kwargs['want'],201);self.assertEqual(second.kwargs['index'],1);self.assertEqual(read.args[1],'GET');self.assertEqual(x.call.call_count,3)
    def test_service_only_HTTP_prerequisite_with_identity_grants_no_seed_business(self):
        x=self.chain();x.call=Mock();x.marker_case=Mock();x.sql.side_effect=[json.dumps({'app':'app-owned','env':'env-owned'}),''];x.inputs()
        self.assertEqual(x.call.call_args.args[:3],('admin','POST','/api/v1/services'));self.assertTrue(x.sql.call_args_list[0].kwargs['read']);self.assertIn('INSERT INTO v3_role_grants',x.sql.call_args_list[1].args[0]);self.assertNotIn('INSERT INTO investigations',x.sql.call_args_list[1].args[0])
    def test_ledger_fullsix_rows_other_tables_readonly(self):
        x=self.chain();x.sql.side_effect=['["runs","sessions"]','{}'];x.ledger();query=x.sql.call_args.args[0]
        for t in c.TABLES:self.assertIn('FROM public.'+t+' t',query)
        self.assertIn('json_object_agg',query);self.assertTrue(x.sql.call_args.kwargs['read'])
    def test_wire_receipt_synthetic_rawhash_no_cookie_or_bearer(self):
        x=self.chain()
        def wire(*a,**k):return c.base.transport(c.base.BASES[1],'POST','/api/v1/canvases',b'{}',{'Cookie':'never-save'},12)
        with patch.object(c.base.Chain,'call',side_effect=wire),patch.object(c.base,'transport',return_value=(503,b'{"error":{"code":"audit_unavailable"}}',{'Set-Cookie':'never-save'})) as original:
            x.call('reader','POST','/api/v1/canvases',{});self.assertIs(c.base.transport,original)
        text=(self.work/'canvas-wire-1.json').read_text();self.assertNotIn('never-save',text);d=json.loads(text);self.assertEqual(d['API_index'],1);self.assertEqual(len(d['response_sha256']),64)
    def test_source_drift_and_wire_io_failure_stop_without_retry(self):
        x=self.chain();x.frozen.side_effect=c.Blocked('source changed')
        with patch.object(c.base.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):x.call('reader','POST','/api/v1/canvases',{})
        wire.assert_not_called()
        x=self.chain()
        with patch.object(c.base.Chain,'call',side_effect=lambda *a,**k:c.base.transport(c.base.BASES[0],'POST','/api/v1/canvases')),patch.object(c.base,'transport',return_value=(200,b'{}',{})) as wire,patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):x.call('reader','POST','/api/v1/canvases',{})
            self.assertIs(c.base.transport,wire);self.assertEqual(wire.call_count,1)
    def test_private_modules_inventory_no_optin_and_route_limit(self):
        self.assertIsNot(c.base,shared);self.assertEqual(shared.PORTS,(18606,18607));self.assertEqual(c.PORTS,(18844,18845));contract=c.Contract(c.ROOT)
        for method,path in c.base.TARGETS+c.base.SUPPORT:contract.canonical(method,'/api/v1'+path.replace('{id}','own'))
        for method,path in [('POST','/api/v1/incidents'),('POST','/api/v1/canvases/own/nodes/root/messages'),('GET','https://external/api/v1/canvases/own')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
        with patch.object(c,'doctor') as doctor,patch.object(c.os,'open') as opened,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
        doctor.assert_not_called();opened.assert_not_called();self.assertEqual(json.loads(out.getvalue())['normal_added'],0);self.assertEqual(json.loads(out.getvalue())['command_timeout_seconds'],420)
        with patch.object(c,'admission') as admit,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admit.assert_not_called()
    def test_admission_exact_identity_privilege_and_sole_actual_runner(self):
        run=str(uuid.uuid4());p=dict(status='root-admitted-canvas-creation-audit',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},manifest_sha256='c'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,canvas_creation_audit_fault_authorized=True)
        row=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='c'*64,pid=os.getppid());path=c.ROOT/'.local/testlab'/('offline-'+run+'.json')
        def admit(permit,rows):
            with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=os.getuid()))()),patch.object(c.base,'private_json',return_value=permit),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as db:
                db.return_value.execute.return_value=[(json.dumps(r),) for r in rows];return c.admission(path,run)
        admit(p,[row])
        for rows in ([],[row,row],[dict(row,pid=os.getppid()+1)],[dict(row,manifest_sha='d'*64)],[dict(row,selected_suite='other')]):
            with self.assertRaises(AssertionError):admit(p,rows)
        for key in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','canvas_creation_audit_fault_authorized'):
            with self.assertRaises(AssertionError):admit(dict(p,**{key:False}),[row])
        self.assertIn('components.py',c.hashes())
if __name__=='__main__':unittest.main()

"""Offline partial-DTO, actual-clock, empty-store and admission counterexamples."""
import contextlib,copy,datetime,io,json,os,sys,tempfile,threading,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_trace_agent_empty_coverage as c
import api_management_chain as shared
class TraceAgentEmptyTests(unittest.TestCase):
    def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
    def chain(self):
        x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.users={'reader':'b'*32};x.roles={'reader':'own-role'};x.marker='owned';x.admin_email='own-admin@testlab.invalid';x.database='db-own';x.db_user='role-own';x.owner_tag='owner:uuid';x.raw_number=0;x.frozen=Mock();x.owner=Mock();x.sql=Mock();return x
    def dto(self,end='2026-10-06T10:30:00.123456789Z',start='2026-10-05T10:30:00.123456789Z'):
        v=copy.deepcopy(c.STATIC);origin=datetime.datetime.fromisoformat(start.replace('Z','+00:00'));v.update(took='0 ms',window_start_at=start,window_end_at=end,histogram={'counts':[[0,0] for _ in range(12)],'axis_times':[(origin+datetime.timedelta(hours=2*i)).strftime('%Y-%m-%dT%H:%M:%SZ') for i in range(12)]});return v
    def verify(self,value):return c.verify_empty(value,'2026-10-06T10:30:00Z','2026-10-06T10:30:01Z')
    def ledger(self):return dict({t:[] for t in c.TABLES},business={'sessions':'unchanged','runs':'empty','templates':'definitions'})
    def test_exact_empty_partial_real_consumer_shape(self):self.verify(self.dto())
    def test_missing_partial_or_producer_claim_cannot_pass(self):
        for patch in ({'partial':False},{'partial':1},{'missing':[]},{'source':'Tempo'},{'aggregation_scope':'all_runs'},{'items':[{'id':'fabricated'}]},{'total':1}):
            with self.assertRaises(AssertionError):self.verify(dict(self.dto(),**patch))
        bad=self.dto();bad.pop('partial')
        with self.assertRaises(AssertionError):self.verify(bad)
        c.verify_empty(self.dto(),'2026-10-06T10:30:00Z','2026-10-06T10:30:01Z')
    def test_counts_booleans_are_not_numeric_zero(self):
        for key in ('total','excluded_running'):
            with self.assertRaises(AssertionError):self.verify(dict(self.dto(),**{key:False}))
        bad=self.dto();bad['views'][0]['count']=False
        with self.assertRaises(AssertionError):self.verify(bad)
        bad=self.dto();bad['histogram']['counts'][0][0]=False
        with self.assertRaises(AssertionError):self.verify(bad)
    def test_exact_views_facets_and_no_sampled_nonempty_metadata(self):
        for key,value in [('views',[]),('facets',[]),('scatter',[{}]),('colors',{'fake':'blue'}),('histogram_non_success_states',['failed'])]:
            with self.assertRaises(AssertionError):self.verify(dict(self.dto(),**{key:value}))
        bad=self.dto();bad['facets'][0]['items']=[{'value':'made-up','count':0}]
        with self.assertRaises(AssertionError):self.verify(bad)
    def test_took_nonnegative_integer_string_not_fixed_value(self):
        self.verify(dict(self.dto(),took='7 ms'));self.verify(dict(self.dto(),took='1000 ms'))
        for value in ('-1 ms','1.5 ms','01 ms','NaN ms',0):
            with self.assertRaises(AssertionError):self.verify(dict(self.dto(),took=value))
    def test_request_clock_bounds_reject_old_future_frozen_windows(self):
        for shift in (-60,60):
            v=self.dto();origin=datetime.datetime.fromisoformat(v['window_end_at'].replace('Z','+00:00'))+datetime.timedelta(seconds=shift);v['window_end_at']=origin.isoformat().replace('+00:00','Z');v['window_start_at']=(origin-datetime.timedelta(hours=24)).isoformat().replace('+00:00','Z')
            with self.assertRaises(AssertionError):self.verify(v)
    def test_exact24hour_nanosecond_delta_no_float_truncation(self):
        v=self.dto();v['window_start_at']='2026-10-05T10:30:00.123456788Z'
        with self.assertRaises(AssertionError):self.verify(v)
        self.assertEqual(c.timestamp_ns('2026-10-05T10:30:00.123456789Z')-c.timestamp_ns('2026-10-05T10:30:00.123456788Z'),1)
    def test_window_datetime_not_naive_or_nonUTC(self):
        for value in ('2026-10-06T10:30:00','2026-10-06T10:30:00+01:00','2026-10-06T10:30:00.1234567890Z',None):
            with self.assertRaises(AssertionError):c.timestamp_ns(value)
        with self.assertRaises(AssertionError):c.verify_empty(self.dto(),'2026-10-06T10:30:02Z','2026-10-06T10:30:01Z')
        with self.assertRaises(AssertionError):c.verify_empty(self.dto(),'2026-10-06T10:30:00Z','2026-10-06T10:30:01Z',tolerance=60)
    def test12_buckets_axis_derived_not_copied_or_non_numeric(self):
        for key,value in [('axis_times',['2026-10-05T00:00:00Z']*12),('counts',[[0,0]]*11),('counts',[[0,0.0]]*12)]:
            bad=self.dto();bad['histogram'][key]=value
            with self.assertRaises(AssertionError):self.verify(bad)
    def test_twoAPI_window_took_axis_differences_can_be_legitimate(self):
        left=self.verify(self.dto());v=self.dto(end='2026-10-06T10:31:00.123456789Z',start='2026-10-05T10:31:00.123456789Z');v['took']='5 ms';right=c.verify_empty(v,'2026-10-06T10:31:00Z','2026-10-06T10:31:01Z');self.assertEqual(left,right)
        self.assertNotEqual(self.dto()['histogram']['axis_times'],v['histogram']['axis_times'])
    def test_full_run_span_feedback_audit_and_public_residue_guard(self):
        before=self.ledger();c.same(before,copy.deepcopy(before))
        for key in before:
            bad=copy.deepcopy(before);bad[key]=[{'residue':'owned'}] if key!='business' else {'sessions':'changed'}
            with self.assertRaises(AssertionError):c.same(before,bad)
    def test_seed_only_one_human_three_exact_environment_grants(self):
        x=self.chain();expected=dict(actor=x.users['reader'],grants=[dict(module=m,action='view',scope_kind='environment',scope_id='test') for m in ('agents','environment','traces')]);x.sql.side_effect=['',json.dumps(expected)];x.marker_case=Mock();x.seed();query=x.sql.call_args_list[0].args[0]
        for term in ('INSERT INTO users','INSERT INTO rbac_roles','INSERT INTO rbac_user_roles','INSERT INTO v3_role_grants'):self.assertIn(term,query)
        for table in c.TABLES:self.assertNotIn('INSERT INTO '+table+'(',query)
        self.assertNotIn("'global'",query);self.assertNotIn("'change'",query);self.assertEqual(expected['grants'][0]['scope_id'],'test')
    def test_unexpected_grant_does_not_pass_identity_readback(self):
        x=self.chain();x.sql.side_effect=['',json.dumps(dict(actor=x.users['reader'],grants=[]))]
        with self.assertRaises(AssertionError):x.seed()
    def test_full_ledger_readonly_actual_schema_table_names(self):
        x=self.chain();x.sql.side_effect=['["sessions","runs"]','{}'];x.ledger();query=x.sql.call_args.args[0]
        for table in c.TABLES:self.assertIn('FROM public.'+table+' t',query)
        self.assertIn('json_object_agg',query);self.assertTrue(x.sql.call_args.kwargs['read'])
    def test_valid_read_exact_cache_and_static_then_no_mutation(self):
        x=self.chain();before=self.ledger();x.ledger=Mock(return_value=before);x.last_window=('2026-10-06T10:30:00Z','2026-10-06T10:30:01Z');x.call=Mock(return_value=(self.dto(),{'Cache-Control':'no-store'}));x.marker_case=Mock();self.assertEqual(x.read(1),self.verify(self.dto()));self.assertEqual(x.call.call_args.kwargs['index'],1)
        x.call.return_value=(self.dto(),{})
        with self.assertRaises(AssertionError):x.read(1)
    def unqualified_wire_counterexample(self,value):
        # Exercise the real base.call ordering: schema-valid 200 is emitted
        # before read() can reject the semantic DTO/window or compare ledger.
        x=self.chain();x.contract=c.Contract(c.ROOT);x.lock=threading.RLock();x.calls=0;x.cookies={'reader':'own-cookie-not-saved'};x.run_id=str(uuid.uuid4());x.first_red=Mock();x.ledger=Mock(return_value=self.ledger());x.marker_case=Mock()
        c.Contract(c.ROOT).shape('GET',c.EXACT_QUERY,200,value)
        with patch.object(c.base,'transport',return_value=(200,json.dumps(value).encode(),{'Cache-Control':'no-store'})),patch.object(c,'utc_now',side_effect=['2026-10-06T10:30:00Z','2026-10-06T10:30:01Z']),contextlib.redirect_stdout(io.StringIO()) as out:
            with self.assertRaises(AssertionError):x.read(0)
        row=json.loads((x.work/'interfaces.jsonl').read_text());emitted=[json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(row['status'],'passed');self.assertEqual(row['actual']['status'],200);self.assertEqual(x.calls,1)
        self.assertFalse(row['registration_evidence']);self.assertTrue(all(event.get('interface',{}).get('registration_evidence') is False for event in emitted));x.marker_case.assert_not_called();x.first_red.assert_not_called()
    def test_openapi200_partialfalse_never_registers_before_semantic_failure(self):
        self.unqualified_wire_counterexample(dict(self.dto(),partial=False))
    def test_openapi200_oldwindow_never_registers_before_window_failure(self):
        self.unqualified_wire_counterexample(self.dto(end='2026-10-05T10:30:00.123456789Z',start='2026-10-04T10:30:00.123456789Z'))
    def test_anonymous_and_actual_invalid_query_refusals_snapshot_unchanged(self):
        x=self.chain();x.ledger=Mock(return_value=self.ledger());x.call=Mock();x.refusal(None,c.EXACT_QUERY,401,'unauthorized');self.assertEqual(x.call.call_args.kwargs['scenario'],'authorization')
        for path,code in c.INVALID_QUERIES:x.refusal('reader',path,422,code)
        self.assertEqual(x.call.call_count,5);self.assertTrue(all('scope=agent' in path for path,_ in c.INVALID_QUERIES))
    def test_raw_wire_receipt_actual_bounds_and_no_cookie(self):
        x=self.chain()
        with patch.object(c.base.Chain,'call',side_effect=lambda *a,**k:c.base.transport(c.base.BASES[1],'GET',c.EXACT_QUERY,None,{'Cookie':'secret-never-save'},12)),patch.object(c.base,'transport',return_value=(200,b'{}',{'Cache-Control':'no-store','Set-Cookie':'secret-never-save'})) as wire,patch.object(c,'utc_now',side_effect=['2026-10-06T10:30:00Z','2026-10-06T10:30:01Z']):
            x.call('reader','GET',c.EXACT_QUERY);self.assertIs(c.base.transport,wire)
        text=(x.work/'trace-wire-1.json').read_text();self.assertNotIn('secret-never-save',text);row=json.loads(text);self.assertEqual(row['API_index'],1);self.assertEqual(row['request_wall_start'],x.last_window[0]);self.assertEqual(len(row['response_sha256']),64)
    def test_raw_evidence_io_failure_restores_transport_no_retry(self):
        x=self.chain()
        with patch.object(c.base.Chain,'call',side_effect=lambda *a,**k:c.base.transport(c.base.BASES[0],'GET',c.EXACT_QUERY)),patch.object(c.base,'transport',return_value=(200,b'{}',{})) as wire,patch.object(c.base,'_private',side_effect=OSError('full')):
            with self.assertRaises(OSError):x.call('reader','GET',c.EXACT_QUERY)
            self.assertIs(c.base.transport,wire);self.assertEqual(wire.call_count,1)
    def test_source_drift_blocks_wire_before_transport(self):
        x=self.chain();x.frozen.side_effect=c.Blocked('source drift')
        with patch.object(c.base.Chain,'call') as wire:
            with self.assertRaises(c.Blocked):x.call('reader','GET',c.EXACT_QUERY)
        wire.assert_not_called()
    def test_cleanup_one40_and_no_fault_or_models_or_extra_source(self):
        x=self.chain()
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
        self.assertEqual(x.cleanup_deadline,140);cleanup.assert_called_once_with(x)
    def test_cleanup_failure_not_green_or_retried(self):
        x=self.chain()
        with patch.object(c,'bounded_cleanup',side_effect=c.Blocked('exact cleanup failed')) as cleanup,patch.object(c,'emit') as emit:
            with self.assertRaises(c.Blocked):x.cleanup()
        cleanup.assert_called_once_with(x);emit.assert_not_called()
    def test_inventory_and_contract_only_bounded_agent_queries(self):
        self.assertIsNot(c.base,shared);self.assertEqual(shared.PORTS,(18606,18607));contract=c.Contract(c.ROOT)
        for path in (c.EXACT_QUERY,*(p for p,_ in c.INVALID_QUERIES)):contract.canonical('GET',path)
        for method,path in [('GET','/api/v1/traces?scope=app'),('GET','/api/v1/traces/foreign'),('GET','https://external'+c.EXACT_QUERY),('POST','/api/v1/services')]:
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
        with patch.object(c,'doctor') as doctor,patch.object(c.os,'open') as lease,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
        doctor.assert_not_called();lease.assert_not_called();self.assertTrue(json.loads(out.getvalue())['partial'])
        with patch.object(c,'admission') as admit,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admit.assert_not_called()
    def test_root_identity_clock_tolerance_and_sole_runner_guards(self):
        run=str(uuid.uuid4());p=dict(status='root-admitted-trace-agent-empty',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},clock_tolerance_seconds=2,manifest_sha256='c'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,trace_agent_empty_authorized=True)
        row=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='c'*64,pid=os.getppid());path=c.ROOT/'.local/testlab'/('offline-'+run+'.json')
        def admit(permit,rows):
            with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=os.getuid()))()),patch.object(c.base,'private_json',return_value=permit),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as db:db.return_value.execute.return_value=[(json.dumps(r),) for r in rows];return c.admission(path,run)
        admit(p,[row])
        for rows in ([],[row,row],[dict(row,pid=os.getppid()+1)],[dict(row,selected_suite='other')],[dict(row,manifest_sha='d'*64)]):
            with self.assertRaises(AssertionError):admit(p,rows)
        for key in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','clock_tolerance_seconds','fresh_nonce_hmac_process_tuple_verified','headroom_verified','trace_agent_empty_authorized'):
            with self.assertRaises(AssertionError):admit(dict(p,**{key:False}),[row])
        self.assertIn('components.py',c.hashes())
if __name__=='__main__':unittest.main()

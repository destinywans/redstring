"""Offline pair/ledger/admission/cleanup counterexamples only."""
import contextlib,copy,io,json,os,sys,tempfile,threading,time,types,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_user_settings_concurrency_coverage as c

class ConcurrencyTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.work=Path(self.temp.name).resolve()
    def fixture(self,kind='preferences',reverse=False):
        actor,peer='a'*32,'b'*32;prefs=dict(theme='light',locale='en-US',density='comfortable',reduce_motion=True)
        notify=dict(quiet={'enabled':False,'from':'22:00','to':'08:00'},digest='daily',subscriptions=[])
        row=lambda uid:dict(user_id=uid,**prefs,notification_settings=copy.deepcopy(notify),ai_preferences={'memory':True},profile_notifications={},
            updated_at='2026-10-05T01:00:00+00:00',created_at='2026-10-05T00:00:00+00:00')
        before={'settings':[row(actor),row(peer)],'audit':[dict(id=1,action='prior')]};after=copy.deepcopy(before)
        initial=prefs if kind=='preferences' else notify;patches=[{'theme':'dark'},{'density':'compact'}] if kind=='preferences' else [{'quiet':True},{'digest':'weekly'}]
        responses=[None,None];state=copy.deepcopy(initial)
        for index in ((1,0) if reverse else (0,1)):
            state=c.applied(kind,state,patches[index]);responses[index]=copy.deepcopy(state)
            changes={k:patches[index].get(k) for k in c.settings.PREF_KEYS} if kind=='preferences' else dict(quiet=state['quiet']['enabled'],digest=state['digest'],subscriptions=0)
            after['audit'].append(dict(id=index+2,actor_id=actor,actor_kind='user',application_id='',target=actor,action=c.settings.ACTIONS[kind],outcome='success',changes=changes))
        if kind=='preferences':after['settings'][0].update(state)
        else:after['settings'][0]['notification_settings']=state
        after['settings'][0]['updated_at']='2026-10-05T01:00:01+00:00'
        return actor,before,after,initial,patches,responses,state
    def verify(self,data,kind):return c.verify_pair(kind,*data[:6])
    def test_both_legal_serialization_orders_preserve_fields(self):
        for kind in ('preferences','notifications'):
            for reverse in (False,True):
                actor,before,after,initial,patches,responses,final=self.fixture(kind,reverse)
                self.assertEqual(c.verify_pair(kind,before,after,actor,initial,patches,responses),final)
                self.assertNotEqual(responses[0],responses[1])
    def test_lost_final_field_detected_even_when_both_calls_200(self):
        for kind in ('preferences','notifications'):
            actor,before,after,initial,patches,responses,_=self.fixture(kind)
            if kind=='preferences':after['settings'][0]['density']='comfortable'
            else:after['settings'][0]['notification_settings']['digest']='daily'
            with self.assertRaises(AssertionError):c.verify_pair(kind,before,after,actor,initial,patches,responses)
    def test_first_response_need_not_have_final_state(self):
        actor,before,after,initial,patches,responses,final=self.fixture('notifications')
        self.assertEqual(responses[0]['digest'],'daily');self.assertEqual(final['digest'],'weekly')
        c.verify_pair('notifications',before,after,actor,initial,patches,responses)
        stale=[c.applied('notifications',initial,p) for p in patches]
        with self.assertRaises(AssertionError):c.verify_pair('notifications',before,after,actor,initial,patches,stale)
    def test_unsubmitted_columns_and_other_owner_unchanged(self):
        for kind in ('preferences','notifications'):
            actor,before,after,initial,patches,responses,_=self.fixture(kind)
            for index,key,bad in ((0,'ai_preferences',{}),(0,'created_at','later'),(0,'locale','zh-CN'),(1,'theme','dark')):
                changed=copy.deepcopy(after);changed['settings'][index][key]=bad
                with self.assertRaises(AssertionError):c.verify_pair(kind,before,changed,actor,initial,patches,responses)
    def test_full_prior_audit_and_exactly_two_preserved(self):
        actor,before,after,initial,patches,responses,_=self.fixture()
        for audit in (after['audit'][1:],after['audit']+[dict(after['audit'][-1],id=4)],after['audit'][:-1]):
            with self.assertRaises(AssertionError):c.verify_pair('preferences',before,dict(after,audit=audit),actor,initial,patches,responses)
    def test_audit_exact_actor_target_changes_without_schedule_order(self):
        for kind in ('preferences','notifications'):
            actor,before,after,initial,patches,responses,_=self.fixture(kind)
            changed=copy.deepcopy(after);changed['audit'].reverse();c.verify_pair(kind,before,changed,actor,initial,patches,responses)
            for key,bad in (('actor_id','other'),('target','other'),('outcome','recorded'),('changes',{})):
                changed=copy.deepcopy(after);changed['audit'][-1][key]=bad
                with self.assertRaises(AssertionError):c.verify_pair(kind,before,changed,actor,initial,patches,responses)
    def test_timestamp_compared_with_baseline_not_audit_order(self):
        actor,before,after,initial,patches,responses,_=self.fixture(reverse=True)
        after['settings'][0]['updated_at']=before['settings'][0]['updated_at']
        with self.assertRaises(AssertionError):c.verify_pair('preferences',before,after,actor,initial,patches,responses)
    def test_pair_release_is_at_wire_and_no_lock_covers_network(self):
        pair=c.Pair('preferences','PATCH','/api/v1/me/preferences',[{'theme':'dark'},{'density':'compact'}]);arrived=threading.Barrier(2);results=[None,None]
        def fake(*args):arrived.wait(timeout=1);time.sleep(.01);return 200,b'{}',{}
        def worker(index):results[index]=pair.send(index,c.ro.BASES[index],pair.method,pair.path,json.dumps(pair.patches[index]).encode(),{},12)
        with patch.object(c,'_original_transport',side_effect=fake):
            jobs=[threading.Thread(target=worker,args=(i,)) for i in (0,1)]
            for job in jobs:job.start()
            pair.barrier.wait(timeout=1)
            for job in jobs:job.join(timeout=2)
        self.assertTrue(all(v[0]==200 for v in results));pair.verify_overlap()
        with self.assertRaises(AssertionError):pair.send(0,c.ro.BASES[0],pair.method,pair.path,b'{"theme":"dark"}',{},12)
    def test_no_overlap_is_blocked_without_retry(self):
        pair=c.Pair('preferences','PATCH','/api/v1/me/preferences',[{},{}]);pair.wires=[dict(status=200,started_ns=1,finished_ns=2),dict(status=200,started_ns=3,finished_ns=4)]
        with self.assertRaises(c.Blocked):pair.verify_overlap()
    def test_pair_rejects_same_api_wrong_patch_or_path(self):
        pair=c.Pair('preferences','PATCH','/api/v1/me/preferences',[{'theme':'dark'},{'density':'compact'}])
        for args in ((1,c.ro.BASES[0],pair.method,pair.path,b'{"density":"compact"}'),(0,c.ro.BASES[0],pair.method,pair.path,b'{}'),(0,c.ro.BASES[0],pair.method,'/api/v1/me',b'{"theme":"dark"}')):
            with self.assertRaises(AssertionError):pair.send(*args,{},12)
    def test_private_call_global_references_barrier_transport(self):
        self.assertIs(c.ro.Chain.call.__globals__['transport'],c.transport)
        self.assertIn('call_number',c.ro.Chain.call.__code__.co_varnames)
    def test_unique_call_ids_do_not_serialize_requests(self):
        chain=c.Chain.__new__(c.Chain);chain.calls=0;chain.call_id_lock=threading.Lock();ids=[];lock=threading.Lock();barrier=threading.Barrier(2)
        def worker():
            number=chain.next_call();barrier.wait(timeout=1)
            with lock:ids.append(number)
        jobs=[threading.Thread(target=worker) for _ in range(2)]
        for job in jobs:job.start()
        for job in jobs:job.join(timeout=2)
        self.assertEqual(sorted(ids),[1,2]);self.assertEqual(chain.calls,2)
    def test_cleanup_worker_wait_and_original_cleanup_share_one_deadline(self):
        chain=c.Chain.__new__(c.Chain);thread=Mock(ident=1);thread.is_alive.return_value=False;chain.threads=[thread];chain.pairs=[Mock()]
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,contextlib.redirect_stdout(io.StringIO()):chain.cleanup()
        self.assertEqual(chain.cleanup_deadline,140);thread.join.assert_called_once_with(15);cleanup.assert_called_once_with(chain)
        self.assertIn('cleanup_deadline',c.bounded_cleanup.__code__.co_names)
    def test_worker_cleanup_failure_still_attempts_original_cleanup(self):
        chain=c.Chain.__new__(c.Chain);thread=Mock(ident=1);thread.join.side_effect=RuntimeError('worker wait failed');chain.threads=[thread];chain.pairs=[]
        with patch.object(c.time,'monotonic',return_value=100),patch.object(c,'bounded_cleanup') as cleanup,self.assertRaises(RuntimeError):chain.cleanup()
        cleanup.assert_called_once_with(chain);self.assertEqual(chain.cleanup_deadline,140)
    def test_original_ledger_is_readonly_and_full(self):
        chain=c.Chain.__new__(c.Chain);chain.frozen=Mock();chain.owner=Mock();chain.sql=Mock(return_value='{}');chain.ledger()
        query=chain.sql.call_args.args[0];self.assertNotIn('WHERE',query);self.assertIn('v3_user_settings',query);self.assertIn('management_audit',query);self.assertTrue(chain.sql.call_args.kwargs['read'])
    def test_inventory_has_no_actual_resources_and_no_new_normal_count(self):
        with patch.object(c.settings,'doctor') as doctor,patch.object(c.sqlite3,'connect') as database,patch.object(c.os,'open') as opened,contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(c.main(['--inventory']),0)
        self.assertEqual(json.loads(output.getvalue())['http_calls'],16);doctor.assert_not_called();database.assert_not_called();opened.assert_not_called()
    def test_optin_before_lease_or_doctor(self):
        with patch.object(c,'admission') as admission,patch.object(c.settings,'doctor') as doctor,contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):c.main([])
        admission.assert_not_called();doctor.assert_not_called()
    def test_canonical_passed_settings_and_readonly_modules_unchanged(self):
        import api_user_settings_audit_coverage as passed
        import api_readonly_chain as shared
        self.assertIsNot(c.settings,passed);self.assertIsNot(c.ro,passed.ro);self.assertIsNot(c.ro,shared)
        self.assertEqual(shared.PORTS,(18602,18603));self.assertNotIn('concurrent_partial_update',shared.SCENARIO_ALIASES)
        self.assertNotIn('call_number',shared.Chain.call.__code__.co_varnames)
    def test_contract_cannot_fabricate_preference_GET_or_empty_patch_read(self):
        contract=c.settings.Contract(c.ROOT)
        for method,path in (('GET','/api/v1/me/preferences'),('GET','/api/v1/me'),('PUT','/api/v1/me/ai-preferences')):
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
        actor,before,after,initial,patches,responses,_=self.fixture()
        with self.assertRaises(AssertionError):c.verify_pair('preferences',before,after,actor,initial,[{'theme':'dark'},{'theme':'light'}],responses)
    def test_transport_exception_is_not_retried(self):
        pair=c.Pair('preferences','PATCH','/api/v1/me/preferences',[{'theme':'dark'},{'density':'compact'}]);pair.barrier=Mock()
        with patch.object(c,'_original_transport',side_effect=TimeoutError('first transport red')) as transport,self.assertRaises(TimeoutError):
            pair.send(0,c.ro.BASES[0],pair.method,pair.path,b'{"theme":"dark"}',{},12)
        self.assertEqual(transport.call_count,1);self.assertIn('finished_ns',pair.wires[0]);self.assertNotIn('status',pair.wires[0])
    def test_actual_concurrency_receipts_are_idempotency_scope(self):
        self.assertEqual(c.ro.SCENARIO_ALIASES['concurrent_partial_update'],'idempotency')
        row=c.evidence(dict(scenario='idempotency',scenario_detail='concurrent_partial_update'))
        self.assertEqual(row['binary_sha256'],c.BINARY);self.assertIn('no new normal count',row['proof_scope'])
    def test_admission_requires_private_file_and_single_current_runner(self):
        run_id=str(uuid.uuid4());manifest='a'*64;hashes={'owned':'sha'}
        permit=dict(status='root-admitted-user-settings-concurrency',owner=c.ro.OWNER,resource_run_id=run_id,source_tree_sha=c.SOURCE,
            binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256=hashes,
            manifest_sha256=manifest,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True)
        active=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha=manifest,pid=os.getppid())
        path=self.work/'.local/testlab/admission.json';path.parent.mkdir(parents=True);path.write_text(json.dumps(permit));path.chmod(0o600)
        connection=Mock();connection.execute.return_value=[(json.dumps(active),)]
        with patch.object(c,'ROOT',self.work),patch.object(c,'hashes',return_value=hashes),patch.object(c.sqlite3,'connect',return_value=connection):
            self.assertEqual(c.admission(path,run_id),permit)
            for field,bad in (('selected_suite','other'),('manifest_sha','b'*64),('source_tree_sha','b'*64),('pid',os.getppid()+1)):
                connection.execute.return_value=[(json.dumps(dict(active,**{field:bad})),)]
                with self.assertRaises(AssertionError):c.admission(path,run_id)
            connection.execute.return_value=[(json.dumps(active),),(json.dumps(active),)]
            with self.assertRaises(AssertionError):c.admission(path,run_id)
            path.chmod(0o644)
            with self.assertRaises(AssertionError):c.admission(path,run_id)
    def test_source_drift_blocks_before_any_http(self):
        chain=c.Chain.__new__(c.Chain);chain.frozen=Mock(side_effect=c.Blocked('source drift'))
        with patch.object(c.ro.Chain,'call') as call,self.assertRaises(c.Blocked):chain.call('reader','PATCH','/api/v1/me/preferences',{'theme':'dark'})
        call.assert_not_called()

if __name__=='__main__':unittest.main()

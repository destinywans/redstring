import contextlib, copy, io, json, sys, tempfile, threading, unittest
from pathlib import Path
from unittest.mock import Mock, patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_alert_policy_functional_coverage as policy

class AlertPolicyProtectionTests(unittest.TestCase):
    def test_module_has_private_ports_and_exact_normal_inventory(self):
        self.assertEqual(policy.PORTS,(18632,18633))
        self.assertEqual(policy.native.PORTS,policy.PORTS)
        self.assertEqual(policy.TARGETS,(('GET','/alert-groups/{id}/policy'),('GET','/inhibitions')))
    def test_read_relay_only_permits_actual_am_status(self):
        policy.allowed_relay('GET','/api/v2/status')
        for method,path in [('POST','/api/v2/status'),('GET','/api/v2/silences'),('GET','http://external/api/v2/status'),('GET','//api/v2/status'),('GET','/api/v2/status?extra=1')]:
            with self.subTest(path=path),self.assertRaises(policy.Blocked):policy.allowed_relay(method,path)
    def test_config_uses_only_http_produced_uuid_scope_and_no_history(self):
        value=policy.source_configuration('policy-'+('a'*32),'b'*32,'c'*32,'scope-'+('a'*32),Path('/private/source-token'),Path('/private/am-token'))
        self.assertEqual(value['operations']['sources'][0]['mappings'][0]['application_id'],'b'*32)
        self.assertEqual(value['alertmanager']['environments']['test']['application_bindings'],{'policy-'+('a'*32):'b'*32})
        self.assertFalse(value['operations']['feishu_channels']);self.assertEqual(set(value),{'operations','alertmanager'})
        for app,env in [('', 'c'*32),('testlab','c'*32),('b'*32,'')]:
            with self.assertRaises(AssertionError):policy.source_configuration('policy-'+('a'*32),app,env,'scope',Path('/source'),Path('/am'))
    def policy_value(self):
        return {'group_id':'group:'+'a'*24,'group_by':['application','environment','alertname'],'route':'testlab-capture','receivers':['testlab-capture'],'timing':[{'key':k,'value':v}for k,v in [('group_wait','1s'),('group_interval','3s'),('repeat_interval','1h')]],'inhibition':None,'policy_source':'configured_alertmanager','policy_observed_at':'2026-10-01T10:00:00Z','local_grouping':['environment_id','application_id','source_id','fingerprint'],'timeline':[],'timeline_scope':'platform_queue','timeline_truncated':False,'partial':True,'unavailable':['inhibition','timeline[].done','external_notification_history','receiver_acknowledgement']}
    def test_policy_verifies_actual_timing_and_preserves_missing_delivery_proof(self):
        value=self.policy_value();policy.verify_policy(value,value['group_id'])
        for key,bad in [('group_by',[]),('route','external'),('receivers',['other']),('timeline',[{'done':True}]),('partial',False),('inhibition',{}),('unavailable',[])]:
            altered=copy.deepcopy(value);altered[key]=bad
            with self.subTest(key=key),self.assertRaises(AssertionError):policy.verify_policy(altered,value['group_id'])
    def test_empty_inhibition_list_requires_real_revision_and_honest_partial_fields(self):
        rev='sha256:'+'a'*64
        value={'items':[],'partial':True,'unavailable':['activity','hit_counts'],'unavailable_environments':[],'unscoped_present':False,'config_revisions':{'test':rev},'observed_at':{'test':'2026-10-01T10:00:00Z'}}
        policy.verify_inhibitions(value,rev)
        for key,bad in [('items',[{}]),('partial',False),('unavailable',[]),('config_revisions',{'test':'sha256:'+'b'*64}),('observed_at',{})]:
            altered=copy.deepcopy(value);altered[key]=bad
            with self.subTest(key=key),self.assertRaises(AssertionError):policy.verify_inhibitions(altered,rev)
    def test_source_status_without_rules_cannot_be_promoted_to_nonempty_inhibition(self):
        raw={'config':{'original':'route:\n  receiver: local\nreceivers:\n- name: local\n'}}
        self.assertEqual(policy.source_revision(raw),'sha256:'+policy.hashlib.sha256(raw['config']['original'].encode()).hexdigest())
        for bad in ({},{'config':{'original':''}},{'config':{'original':'route:\n  routes:\n  - receiver: other\n'}}):
            with self.assertRaises(policy.Blocked):policy.source_revision(bad)
    def test_blockers_do_not_claim_mock_or_success(self):
        out=io.StringIO()
        with contextlib.redirect_stdout(out):policy.emit_blockers()
        rows=[json.loads(line)for line in out.getvalue().splitlines()]
        self.assertEqual(len(rows),2);self.assertTrue(all(r['status']=='blocked'and r['evidence_mode']=='real_local'for r in rows))
        self.assertEqual({r['actual']['error_code']for r in rows},{'route_source_unavailable','not_connected'})
    def test_canonical_routes_are_bounded_and_partial_status_matches_contract(self):
        contract=policy.Contract(policy.ROOT)
        self.assertEqual(contract.canonical('GET','/api/v1/alert-groups/group:'+'a'*24+'/policy')[0],'/api/v1/alert-groups/{id}/policy')
        with self.assertRaises(policy.Blocked):contract.canonical('POST','/api/v1/inhibitions')
        value={'items':[],'partial':True,'unavailable':['activity','hit_counts'],'unavailable_environments':[],'unscoped_present':False,'config_revisions':{'test':'sha256:'+'a'*64},'observed_at':{'test':'2026-10-01T10:00:00Z'}}
        contract.shape('GET','/api/v1/inhibitions',206,value)
    def test_pg_recreation_stops_before_sql(self):
        chain=policy.Chain.__new__(policy.Chain);chain.data={'postgres_container':'owned'}
        with patch.object(policy.native,'inspect_pg',return_value={'Id':'foreign'}),patch.object(policy.native.Chain,'pg') as parent,self.assertRaises(policy.Blocked):chain.pg('SELECT 1')
        parent.assert_not_called()
    def test_started_helpers_are_frozen_without_global_shared_namespace_edits(self):
        import api_native_projection_coverage as shared
        self.assertNotEqual(shared.PORTS,policy.native.PORTS)
        self.assertNotEqual(shared.TARGETS,policy.native.TARGETS)
        self.assertEqual(policy.native.TARGETS,policy.TARGETS)

    def fake_chain(self,root):
        chain=policy.Chain.__new__(policy.Chain);chain.root=chain.work=Path(root);chain.contract=policy.Contract(policy.ROOT)
        chain.users={'reader':'own'};chain.cookies={};chain.calls=0;chain.lock=threading.RLock();chain.run_id='a'*32;chain.source_id='own'
        chain.data={'source_tree_sha':'frozen','binary_sha256':'b'*64,'helper_sha256':{'helper':'c'*64}};chain.frozen=Mock()
        return chain
    def inhibition_value(self):
        return {'items':[],'partial':True,'unavailable':['activity','hit_counts'],'unavailable_environments':[],'unscoped_present':False,'config_revisions':{'test':'sha256:'+'a'*64},'observed_at':{'test':'2026-10-01T10:00:00Z'}}
    def test_actual_emitter_registers_only_semantically_verified_response_and_private_original(self):
        with tempfile.TemporaryDirectory()as root:
            chain=self.fake_chain(root);output=io.StringIO();value=self.inhibition_value()
            with patch.object(policy,'ACTIVE_CHAIN',chain),patch.object(policy,'_original_transport',return_value=(206,json.dumps(value).encode(),{'ETag':'"sha256:'+'a'*64+'"'})),contextlib.redirect_stdout(output):
                chain.call('reader','GET','/api/v1/inhibitions',want=206,validate=lambda v:policy.verify_inhibitions(v,'sha256:'+'a'*64))
            row=json.loads(output.getvalue());self.assertTrue(row['interface']['registration_evidence'])
            original=json.loads((Path(root)/'http-response-1.json').read_text());self.assertEqual(original['response'],value)
            self.assertEqual(original['call_id'],chain.run_id+'-1');self.assertEqual((Path(root)/'http-response-1.json').stat().st_mode&0o777,0o600)
    def test_native_normal_status_with_bad_semantics_is_failure_and_not_registered(self):
        with tempfile.TemporaryDirectory()as root:
            chain=self.fake_chain(root);output=io.StringIO();value=self.inhibition_value();value['items']=[{}]
            with patch.object(policy,'ACTIVE_CHAIN',chain),patch.object(policy,'_original_transport',return_value=(206,json.dumps(value).encode(),{})),contextlib.redirect_stdout(output),self.assertRaises(AssertionError):
                chain.call('reader','GET','/api/v1/inhibitions',want=206,validate=lambda v:policy.verify_inhibitions(v,'sha256:'+'a'*64))
            row=json.loads(output.getvalue());self.assertEqual(row['status'],'failed');self.assertFalse(row['interface']['registration_evidence'])
            self.assertTrue((Path(root)/'first-failure.json').is_file())
    def test_relay_cleanup_before_start_never_calls_shutdown_on_unstarted_thread(self):
        relay=policy.Relay.__new__(policy.Relay);relay.closed=False;relay.release=Mock();relay.thread=Mock();relay.thread.is_alive.return_value=False;relay.server=Mock()
        relay.close();relay.close();relay.server.shutdown.assert_not_called();relay.server.server_close.assert_called_once()
    def test_revoked_policy_object_is_hidden_before_and_after_real_provider_boundary(self):
        with tempfile.TemporaryDirectory()as root:
            chain=self.fake_chain(root);chain.roles={'reader':'uuid-owned-role'};chain.sql=Mock();chain.normal=Mock()
            chain.relay=Mock(lock=threading.RLock(),seen=threading.Event(),release=threading.Event())
            calls=[]
            def answer(*args,**kwargs):
                calls.append(kwargs)
                if kwargs.get('index',0)==0:
                    chain.relay.seen.set();self.assertTrue(chain.relay.release.wait(2))
                self.assertEqual(kwargs['want'],404);self.assertEqual(kwargs['code'],'not_found')
            with patch.object(chain,'call',side_effect=answer),contextlib.redirect_stdout(io.StringIO()):
                chain.revocation('/api/v1/alert-groups/group:'+'a'*24+'/policy')
            self.assertEqual(len(calls),2);chain.normal.assert_called_once()

if __name__=='__main__':unittest.main()

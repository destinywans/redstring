"""Ownership-before-delete and software cryptography tests; no Docker/API invocation."""
import importlib.util,json,sys,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import gitops
chain_spec=importlib.util.spec_from_file_location('testlab_gitops_chain',Path(__file__).with_name('chain.py'))
chain=importlib.util.module_from_spec(chain_spec);chain_spec.loader.exec_module(chain)

class ChainEvidenceTest(unittest.TestCase):
    def pending_execution(self):
        return {'proposal_id':'p','execution_id':'a'*32,'state':'pending_review','deployment':{'project_id':42,'mr_iid':7,'source_branch':'aiops-alert-rule-enable/'+'a'*32,'target_branch':'main','path':'workload/rules.yml','state':'opened','content_digest':'b'*64,'url':'https://localhost:18580/ops/rules/-/merge_requests/7'}}
    def test_worker_must_persist_pending_review_before_merge(self):
        for execution in (None,{**self.pending_execution(),'state':'uncertain','deployment':{}},{**self.pending_execution(),'state':'exporting'}):
            self.assertIsNone(chain.worker_review_receipt(execution,'p','a'*32,42,'https://localhost:18580'))
        execution=self.pending_execution()
        self.assertEqual(chain.worker_review_receipt(execution,'p','a'*32,42,'https://localhost:18580'),execution['deployment'])
        execution['deployment']=json.dumps(execution['deployment'])
        self.assertEqual(chain.worker_review_receipt(execution,'p','a'*32,42,'https://localhost:18580')['mr_iid'],7)
    def test_pending_worker_receipt_cannot_substitute_scope_or_authority(self):
        for field,value in (('mr_iid',None),('mr_iid',True),('project_id',43),('source_branch','other'),('target_branch','other'),('path','other'),('state','merged'),('content_digest','unknown'),('url','https://other.invalid/ops/rules/-/merge_requests/7')):
            execution=self.pending_execution();execution['deployment'][field]=value
            with self.subTest(field=field,value=value),self.assertRaises(RuntimeError):chain.worker_review_receipt(execution,'p','a'*32,42,'https://localhost:18580')
        for field in ('proposal_id','execution_id'):
            execution=self.pending_execution();execution[field]='other'
            with self.assertRaisesRegex(RuntimeError,'durable approved authority'):chain.worker_review_receipt(execution,'p','a'*32,42,'https://localhost:18580')
    def test_first_vote_stays_pending_without_execution_authority(self):
        vote={'proposal_id':'p','status':'pending_review','required_approvals':2,'received_approvals':1}
        observation={'approval_decision_exists':False,'enable_execution_exists':False}
        chain.validate_first_vote(vote,observation,'p')
        for changes in ({'status':'pending'},{'status':'approved'},{'execution_id':'a'*32}):
            with self.assertRaises(RuntimeError):chain.validate_first_vote({**vote,**changes},observation,'p')
    def test_first_vote_cannot_create_decision_or_execution_in_database(self):
        vote={'proposal_id':'p','status':'pending_review','required_approvals':2,'received_approvals':1}
        for key in ('approval_decision_exists','enable_execution_exists'):
            observation={'approval_decision_exists':False,'enable_execution_exists':False};observation[key]=True
            with self.assertRaisesRegex(RuntimeError,'already created'):chain.validate_first_vote(vote,observation,'p')
    def test_second_vote_binds_actual_two_actors_and_execution(self):
        vote={'proposal_id':'p','status':'approved','execution_id':'a'*32,'required_approvals':2,'received_approvals':2}
        observed={'creator_id':'creator','execution_id':'a'*32,'required_count':2,'first_approver_id':'reviewer1','second_approver_id':'reviewer2'}
        chain.validate_second_vote(vote,observed,'p','creator',['reviewer1','reviewer2'])
        for changes in ({'first_approver_id':'creator'},{'second_approver_id':'reviewer1'},{'second_approver_id':'stranger'},{'execution_id':'b'*32}):
            with self.assertRaises(RuntimeError):chain.validate_second_vote(vote,{**observed,**changes},'p','creator',['reviewer1','reviewer2'])
        with self.assertRaises(RuntimeError):chain.validate_second_vote({**vote,'execution_id':''},observed,'p','creator',['reviewer1','reviewer2'])
    def test_owned_approval_seed_requires_passkey_and_exact_conflict_scope(self):
        seed=chain.approval_grant_seed('testlab-0123456789ab')
        self.assertEqual(seed,"INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES('gitops-testlab-0123456789ab-approve','proposals','approve','application','testlab','passkey') ON CONFLICT(role_id,module,action,scope_kind,scope_id) DO UPDATE SET assurance=EXCLUDED.assurance;")
    def test_non_owned_role_cannot_be_used_for_approval_seed(self):
        for owner in ('admin',"testlab-0123456789ab'; UPDATE users SET disabled=false; --"):
            with self.assertRaisesRegex(RuntimeError,'owned approval role'):chain.approval_grant_seed(owner)
    def test_enabled_rule_fixture_includes_contract_scope_labels(self):
        investigation='INV-'+'a'*32
        body=chain.enabled_rule_request(investigation,'test','TestlabApprovedFixture')
        self.assertEqual(body['investigation_id'],investigation)
        self.assertEqual(body['labels'],{'application':body['application_id'],'environment':body['environment_id'],'severity':'P3'})
        self.assertEqual(body['mode'],'enabled')
    def test_component_probe_does_not_emit_product_chain_terminal(self):
        state={'project_id':1,'probe_mr':2,'probe_mr_url':'https://localhost/owned/probe'}
        with patch.object(gitops,'http',return_value=(200,{'approved':False,'approved_by':[]})),patch.object(gitops,'save'),patch.object(gitops,'emit') as emit:
            self.assertTrue(gitops.review_probe(state))
        self.assertEqual(emit.call_count,1);self.assertEqual(emit.call_args.args[:2],('gitops.approval_api','passed'))
        self.assertFalse(emit.call_args.args[2]['product_chain_verified_by_this_probe'])
    def test_missing_approval_protocol_remains_blocked(self):
        state={'project_id':1,'probe_mr':2,'probe_mr_url':'https://localhost/owned/probe'}
        with patch.object(gitops,'http',return_value=(404,{})),patch.object(gitops,'save'),patch.object(gitops,'emit') as emit:
            self.assertFalse(gitops.review_probe(state))
        self.assertEqual(emit.call_args.args[:2],('gitops.approval_api','blocked'))
    def test_first_http_error_is_immutable_and_credentials_omitted(self):
        with tempfile.TemporaryDirectory() as folder:
            work=Path(folder);body={'error':{'code':'enable_not_connected','message':'Worker unavailable','token':'secret'},'cookie':'secret'}
            first=chain.record_http_failure(work,'attempt1',1,'POST','/api/v1/alert-rules',503,body)
            before=first.read_text()
            second=chain.record_http_failure(work,'attempt2',1,'POST','/api/v1/alert-rules',422,body)
            self.assertNotEqual(first,second);self.assertEqual(first.read_text(),before)
            self.assertNotIn('secret',before);self.assertEqual(first.stat().st_mode&0o777,0o600)
            with self.assertRaises(FileExistsError):chain.record_http_failure(work,'attempt1',1,'POST','/api/v1/alert-rules',503,body)
            self.assertEqual(json.loads((work/'chain-last-red.json').read_text())['evidence_file'],str(second))
    def test_database_observer_transaction_is_explicitly_read_only(self):
        calls=[]
        output=chain.observe(lambda backend,statement:calls.append(statement) or '{"state":"pending_review"}',{},'SELECT row_to_json(e) FROM executions e;')
        self.assertEqual(output,'{"state":"pending_review"}')
        self.assertTrue(calls[0].startswith('BEGIN READ ONLY;'));self.assertTrue(calls[0].endswith('COMMIT;'))

class HeavyCleanupTest(unittest.TestCase):
    def state(self,root):
        owner='testlab-0123456789ab';work=root/'owned';state={'owner':owner,'work':str(work/'gitops'),'gitlab_name':'aiops-'+owner+'-gitlab','cluster':gitops.CLUSTER}
        path=root/'.local/testlab';path.mkdir(parents=True);(path/'environment.json').write_text(json.dumps({'owner':owner,'work':str(work)}));return state
    def test_pg_absent_does_not_block_only_owned_heavy_cleanup(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);state=self.state(root)
            with patch.object(gitops,'ROOT',root),patch.object(gitops,'owned_container',return_value={'owned':True}),patch.object(gitops,'command') as command,patch.object(gitops,'emit'):
                gitops.cleanup(state)
            self.assertEqual(command.call_count,2)
    def test_second_owner_mismatch_rejects_before_first_removal(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);state=self.state(root)
            with patch.object(gitops,'ROOT',root),patch.object(gitops,'owned_container',side_effect=[{'owned':True},RuntimeError('owner mismatch')]),patch.object(gitops,'command') as command:
                with self.assertRaisesRegex(RuntimeError,'owner mismatch'):gitops.cleanup(state)
            command.assert_not_called()

if __name__=='__main__':unittest.main()

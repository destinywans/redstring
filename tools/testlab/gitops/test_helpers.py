"""Ownership-before-delete and software cryptography tests; no Docker/API invocation."""
import importlib.util,json,sys,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import gitops
chain_spec=importlib.util.spec_from_file_location('testlab_gitops_chain',Path(__file__).with_name('chain.py'))
chain=importlib.util.module_from_spec(chain_spec);chain_spec.loader.exec_module(chain)

class ChainEvidenceTest(unittest.TestCase):
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

import base64
import copy
import contextlib
import hashlib
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_agent_catalog_real_inputs_coverage as real

class RealInputTests(unittest.TestCase):
    def fixture(self):
        data=b'actual owned repository artifact bytes\x00\n';sha=hashlib.sha256(data).hexdigest()
        path='agent-artifacts/v1/bundle.bin';commit='a'*40
        value=dict(file_path=path,commit_id=commit,encoding='base64',size=len(data),content_sha256=sha,
            content=base64.b64encode(data).decode(),blob_id=hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest())
        return value,path,commit,data,sha

    def test_exact_immutable_bytes(self):
        value,path,commit,data,sha=self.fixture()
        self.assertEqual(real.verify_blob(value,path,commit,data),sha)

    def test_rejects_wrong_origin_tuple_and_bytes(self):
        value,path,commit,data,_=self.fixture()
        for field,bad in [('file_path','other'),('commit_id','b'*40),('encoding','text'),('size',len(data)+1),
                ('content_sha256','c'*64),('content',base64.b64encode(b'different').decode()),('blob_id','b'*40)]:
            changed=dict(value,**{field:bad})
            with self.subTest(field=field),self.assertRaises((AssertionError,ValueError)):
                real.verify_blob(changed,path,commit,data)

    def test_gitlab_line_wrapping_allowed_but_invalid_base64_denied(self):
        value,path,commit,data,sha=self.fixture()
        value['content']=value['content'][:8]+'\n'+value['content'][8:]+'\n'
        self.assertEqual(real.verify_blob(value,path,commit,data),sha)
        value['content']+='!'
        with self.assertRaises(ValueError):real.verify_blob(value,path,commit,data)

    def test_path_commit_and_payload_are_bounded_before_acceptance(self):
        value,path,commit,data,_=self.fixture()
        for args in [(value,path,'main',data),(value,'../shared',commit,data),(value,path,commit,b''),
                (value,path,commit,b'x'*4097)]:
            with self.assertRaises(AssertionError):real.verify_blob(*args)

    def test_artifact_manifest_pins_real_bytes_digest_and_commit(self):
        _,_,commit,_,sha=self.fixture()
        value=real.artifact_manifest('agent','app','v1','test-source','c'*32,123,commit,sha,'testlab-promotion-'+'d'*32)
        ref=value['agent_version']['artifact_ref']
        self.assertIn('/root/testlab-promotion-'+'d'*32+'/-/raw/'+commit+'/',ref)
        self.assertTrue(ref.startswith('localhost:18580/'));self.assertTrue(ref.endswith('@sha256:'+sha))
        self.assertNotIn('://',ref)
        self.assertEqual(value['source_digest'],hashlib.sha256(real.base.encoded(value['agent_version'])).hexdigest())
        self.assertNotEqual(sha,hashlib.sha256(b'v1').hexdigest())
        self.assertEqual(value['signature'],'')

    def test_manifest_rejects_unbound_project_commit_or_digest(self):
        _,_,commit,_,sha=self.fixture()
        for pid,rev,digest in [(0,commit,sha),(True,commit,sha),(123,'main',sha),(123,commit,'x'*64)]:
            with self.assertRaises(AssertionError):real.artifact_manifest('agent','app','v1','source','c'*32,pid,rev,digest,'testlab-promotion-'+'d'*32)
        with self.assertRaises(AssertionError):real.artifact_manifest('agent','app','v1','source','c'*32,123,commit,sha,'unowned')

    def test_independent_private_modules_preserve_original_ports_and_mode(self):
        import api_agent_catalog_interfaces_coverage as original
        self.assertIsNot(real.base,original)
        self.assertEqual(original.PORTS,{'source':(18730,18731),'production':(18732,18733)})
        self.assertEqual(real.base.PORTS,real.PORTS);self.assertEqual(real.base.promotion.GITLAB_PROXY_PORT,18760)
        self.assertEqual(original.MODE,'protocol_mock');self.assertEqual(real.base.MODE,'protocol_mock')
        self.assertIs(real.base.admission,real.admission)

    def test_no_live_opt_in_does_not_acquire_resources(self):
        with (mock.patch.object(real,'admission') as admission,mock.patch.object(real.base.promotion,'doctor') as doctor,
              mock.patch.object(real.base.promotion,'exclusive') as lease,contextlib.redirect_stdout(io.StringIO()) as out):
            self.assertEqual(real.main([]),0)
        admission.assert_not_called();doctor.assert_not_called();lease.assert_not_called()
        self.assertEqual(json.loads(out.getvalue())['consumer_mode'],'protocol_mock')

    def test_input_and_consumer_markers_preserve_distinct_evidence_layers(self):
        c=real.Chain.__new__(real.Chain);c.frozen=mock.Mock();c.run_id='c'*32
        with mock.patch.object(real,'emit') as emit:
            c.proof_marker('repo_artifacts',{'native_exporter':False})
            self.assertEqual(emit.call_args.kwargs['evidence_mode'],'real_local')
            c.proof_marker('metadata_contracts',{'routing_connected':False},mode='protocol_mock')
            self.assertEqual(emit.call_args.kwargs['evidence_mode'],'protocol_mock')
            with self.assertRaises(AssertionError):c.proof_marker('whole_pass',{})

    def test_reuse_exact_admission_checks_and_frozen_new_files(self):
        source=Path(real.__file__).read_text()
        admit=source[source.index('def admission('):source.index('base.admission=admission')]
        for guard in ['root-admitted-agent-catalog-real-inputs','fresh_nonce_hmac_process_tuple_verified',
                'headroom_verified','gitlab_recovery_credentials_verified','permanent_own_project_cleanup_authorized',
                'postgres_cid','gitlab_cid','len(active) == 1','os.getppid()','manifest_sha256',
                'gitlab_ca_sha256','backend_fixture_sha256','gitlab_config_sha256']:
            self.assertIn(guard,admit)
        frozen=real.hashes()
        self.assertIn('api_agent_catalog_real_inputs_coverage.py',frozen)
        self.assertIn('tests/test_api_agent_catalog_real_inputs_coverage.py',frozen)

    def test_formal_source_has_metadata_only_checks_and_no_nonprod_agent_registration(self):
        source=(real.ROOT/'internal/v3configsync/write_agent_version.go').read_text()
        self.assertIn('not an attestation of',source)
        self.assertIn('env == "prod" || env == "all"',source)
        self.assertIn('a.stable_version IS NOT NULL',source)
        self.assertIn("VALUES($1,$2,'prod',$3)",source)
        self.assertNotIn('http.NewRequest',source)
        self.assertIn('verifyImportedAgentVersion',source)
        contract=json.loads((real.ROOT/'internal/apicontract/openapi.json').read_text())
        self.assertIn('Stores a pending experiment only',contract['paths']['/evals']['post']['description'])
        self.assertIn('there is no traffic router',contract['paths']['/agents/{id}/rollout']['post']['description'])

    def test_reviewed_artifact_uses_owned_base64_repository_branch_and_distinct_reviewer(self):
        c=real.Chain.__new__(real.Chain);c.projects={'main':{'id':123}};c.prefix='owned-';c.target='main'
        c.assert_project=mock.Mock();c.git=mock.Mock(side_effect=[({},{}),({},{}),({'iid':7},{})]);c.review_merge=mock.Mock()
        item=c.reviewed_file('agent-artifacts/v1/bundle.bin','Ynl0ZXM=','Owned artifact','base64')
        self.assertEqual(item['iid'],7)
        self.assertEqual(c.git.call_args_list[1].args[2]['encoding'],'base64')
        self.assertTrue(all(call.kwargs.get('role')=='proposer' for call in c.git.call_args_list))
        c.review_merge.assert_called_once_with(item)
        parent=(real.ROOT/'tools/testlab/promotion_chain.py').read_text()
        start=parent.index('    def review_merge(')
        review=parent[start:parent.index('\n    def ',start+1)]
        self.assertIn("role='reviewer'",review);self.assertIn("evidence['reviewed_sha']",review)

    def test_imported_artifact_reference_mismatch_stops_before_success_marker(self):
        c=real.Chain.__new__(real.Chain);c.agent='agent';c.artifacts={'v1':{},'v2':{}}
        item=dict(iid=7,manifest={'agent_version':{'artifact_ref':'expected'}})
        c.signed_item=mock.Mock(return_value=item);c.review_merge=mock.Mock();c.call=mock.Mock();c.sql=mock.Mock(return_value='other')
        c.artifact_blob=mock.Mock();c.proof_marker=mock.Mock()
        with self.assertRaises(AssertionError):c.import_real_inputs()
        c.proof_marker.assert_not_called();c.artifact_blob.assert_not_called()

    def test_cleanup_only_reports_after_exact_parent_cleanup_succeeds(self):
        c=real.Chain.__new__(real.Chain)
        with mock.patch.object(real.base.Chain,'cleanup',side_effect=real.Blocked('unresolved exact creation')):
            with mock.patch.object(real,'emit') as emit:
                with self.assertRaises(real.Blocked):c.cleanup()
                emit.assert_not_called()
        with mock.patch.object(real.base.Chain,'cleanup') as parent,mock.patch.object(real,'emit') as emit:
            c.cleanup();parent.assert_called_once();self.assertEqual(emit.call_args.args[0],'agent_real_inputs.cleanup')

    def test_normal_metadata_uses_formal_prompt_name_and_exact_pending_eval_body(self):
        c=real.Chain.__new__(real.Chain);c.agent='agent';c.app='app';c.run_id='c'*32
        c.agent_detail=mock.Mock();c.proof_marker=mock.Mock()
        def call(tier,method,path,body=None,**kwargs):
            if method=='POST' and path=='/api/v1/prompts':
                self.assertEqual(set(body),{'agent_id','name','text'})
                return {'id':'d'*32,'version':'v1'},{}
            if method=='POST' and path=='/api/v1/evals':
                self.assertEqual(set(body),{'agent_id','title','baseline_version','candidate_version'})
                return {'id':'e'*32,'status':'pending','execution_available':False},{}
            return {},{}
        c.call=mock.Mock(side_effect=call);c.metadata_normal()
        c.proof_marker.assert_called_once()
        self.assertEqual(c.proof_marker.call_args.kwargs['mode'],'protocol_mock')
        self.assertFalse(any('/trial' in entry.args[2] for entry in c.call.call_args_list))

if __name__=='__main__':unittest.main()

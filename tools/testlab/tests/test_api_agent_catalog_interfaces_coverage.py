import contextlib
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_agent_catalog_interfaces_coverage as candidate
import promotion_chain as shared

class CatalogTests(unittest.TestCase):
    def test_private_reuse_does_not_change_shared_ports_proxy_or_class(self):
        self.assertIsNot(candidate.promotion, shared)
        self.assertEqual(shared.PORTS, {'source': (18583,18585),'production':(18598,18599)})
        self.assertEqual(shared.GITLAB_PROXY_PORT, 18604)
        self.assertEqual(candidate.promotion.PORTS, candidate.PORTS)
        self.assertEqual(shared.Chain.__module__, 'promotion_chain')

    def test_scope_rejects_execution_eval_cases_legacy_and_remote_urls(self):
        contract=candidate.Contract(candidate.ROOT)
        for method,path in [('POST','/eval-cases'),('GET','/eval-cases/owned'),
                ('GET','/agent-runs/owned'),('POST','/prompts/owned/trial'),
                ('POST','/ai-extensions'),('POST','/promotions'),('POST','/canvases')]:
            with self.assertRaises(candidate.Blocked): contract.canonical(method,'/api/v1'+path)
        with self.assertRaises(candidate.Blocked): contract.canonical('GET','https://remote.invalid/api/v1/agents/owned')
        for method,path in candidate.TARGETS:
            self.assertEqual(contract.canonical(method,'/api/v1'+path.replace('{id}','owned'))[0],'/api/v1'+path)

    def test_manifest_preserves_exact_go_order_digest_and_omits_runtime_data(self):
        value=candidate.manifest('agent-owned','app-owned','v1','source-owned','a'*32)
        self.assertEqual(list(value),['schema','promotion_id','kind','source_id','source_version','source_instance',
            'source_environment','application_id','source_digest','agent_version','signature'])
        self.assertEqual(list(value['agent_version']),['name','version','artifact_ref'])
        self.assertEqual(value['source_digest'],hashlib.sha256(candidate.encoded(value['agent_version'])).hexdigest())
        self.assertEqual(value['signature'],'')
        self.assertNotIn('run',value); self.assertNotIn('prompt',value)
        self.assertEqual(candidate.encoded({'html':'<&>'}),b'{"html":"\\u003c\\u0026\\u003e"}')
        with self.assertRaises(AssertionError): candidate.manifest('agent','app','v1','source','../escape')

    def test_saved_release_validator_rejects_observed_execution_and_missing_unknown(self):
        good=dict(revision=6,routing_connected=False,observed_traffic=None,release_effect='configuration_saved_pending_activation')
        candidate.configuration(good,6)
        for delta in (dict(revision=5),dict(routing_connected=True),dict(observed_traffic=100),dict(release_effect='activated')):
            with self.assertRaises(AssertionError): candidate.configuration(dict(good,**delta),6)
        for key in good:
            bad=dict(good);bad.pop(key)
            with self.assertRaises(AssertionError): candidate.configuration(bad,6)

    def test_pending_eval_validator_rejects_false_results_wrong_versions_and_implicit_unknowns(self):
        good=dict(id='owned',title='pending',status='pending',meta='pending',baseline_version='v1',candidate_version='v2',
            partial=True,missing=['evaluation_result'],**{k:None for k in ('dataset','metrics','radar','cases','datasets','evaluators','result_ref','finished_at')})
        candidate.eval_metadata(good,'owned','pending')
        for delta in (dict(status='passed'),dict(metrics={}),dict(cases=[]),dict(result_ref='fake-proof'),
                dict(baseline_version='v2'),dict(candidate_version='v1'),dict(missing=[])):
            with self.assertRaises(AssertionError):candidate.eval_metadata(dict(good,**delta),'owned','pending')
        bad=dict(good);bad.pop('finished_at')
        with self.assertRaises(AssertionError):candidate.eval_metadata(bad,'owned','pending')

    def bare_chain(self, work):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.frozen=mock.Mock();chain.contract=candidate.Contract(candidate.ROOT)
        chain.work=Path(work);chain.calls=0;chain.cookies={'production':'test-cookie'}
        return chain

    def test_unvalidated_or_business_invalid_2xx_never_becomes_mock_normal_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            chain=self.bare_chain(tmp)
            with mock.patch.object(candidate.promotion,'transport') as wire:
                with self.assertRaises(AssertionError):chain.call('production','GET','/api/v1/agents/owned')
                wire.assert_not_called()
            chain.contract.shape=mock.Mock()
            with (mock.patch.object(candidate.promotion,'transport',return_value=(200,b'{"revision":5}',{})),
                  mock.patch.object(candidate,'emit') as emit):
                with self.assertRaises(AssertionError):chain.call('production','GET','/api/v1/agents/owned',validate=lambda v:candidate.check(v.get('revision')==6,'counterexample'))
                self.assertEqual(emit.call_args.args[1],'failed')
                self.assertEqual(emit.call_args.kwargs['evidence_mode'],'protocol_mock')
                self.assertFalse(emit.call_args.kwargs['interface']['registration_evidence'])
            evidence=json.loads((Path(tmp)/'catalog-http-1.json').read_text())
            self.assertEqual(evidence['evidence_mode'],'protocol_mock')
            self.assertFalse(evidence['request_credentials_recorded'])
            self.assertEqual((Path(tmp)/'catalog-http-1.json').stat().st_mode & 0o777,0o600)

    def test_audit_failure_restores_exact_own_fault_before_propagation(self):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.admin={'production':{'email':'owned@testlab.invalid'}}
        chain.sql=mock.Mock(return_value='owned');chain.snapshot=mock.Mock(return_value={'revision':5})
        def failure():raise AssertionError('original product assertion failure')
        with self.assertRaisesRegex(AssertionError,'original product assertion failure'):
            chain.audit_fault('v3.agent.level',failure)
        self.assertTrue(chain.sql.call_args.args[0].startswith('DROP TRIGGER testlab_catalog_audit_fault'))
        with self.assertRaises(AssertionError):chain.audit_fault('unscoped-any-action',lambda:None)
        self.assertEqual(chain.snapshot.call_count,1)

    def test_unproven_response_loss_cannot_emit_success_or_query_recovery(self):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.agent='owned';chain.prompt='owned-prompt';chain.prompt_text='synthetic'
        chain.app='app';chain.audit_fault=mock.Mock();chain.agent_detail=mock.Mock()
        chain.marker=mock.Mock();chain.snapshot=mock.Mock()
        proxy=mock.Mock(drop_receipt={'path':'/api/v1/agents/other/level','upstream_status':200})
        chain.proxies={'production':proxy};chain.call=mock.Mock(side_effect=[({},{}),OSError('controlled loss')])
        with self.assertRaisesRegex(AssertionError,'loss was not after exact successful product response'):chain.fault_tests()
        self.assertNotIn('response_loss',[c.args[0] for c in chain.marker.call_args_list])
        self.assertEqual(chain.snapshot.call_count,1)  # pre-loss baseline only, never recovery.
        self.assertEqual(chain.agent_detail.call_count,1)

    def test_inventory_and_missing_live_admission_never_allocate_native_resources(self):
        with (mock.patch.object(candidate,'admission') as admission,
              mock.patch.object(candidate.promotion,'doctor') as doctor,
              mock.patch.object(candidate.promotion.Chain,'setup') as setup,
              contextlib.redirect_stdout(io.StringIO())):
            self.assertEqual(candidate.main(['--inventory']),0)
            self.assertEqual(candidate.main(['--live']),1)
        admission.assert_not_called();doctor.assert_not_called();setup.assert_not_called()
        self.assertEqual(candidate.inventory()['accepted_input_mode'],'protocol_mock')
        self.assertFalse(candidate.inventory()['whole_business_chain'])

    def test_revocation_requires_three_validator_backed_viewer_successes_first(self):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.agent='agent';chain.app='app';chain.prompt='prompt';chain.prompt_text='body'
        chain.eval_id='eval';chain.eval_title='title'
        chain.user_cookies={'viewer':'scoped-viewer-cookie'};chain.roles={'viewer':'owned-viewer'}
        chain.snapshot=mock.Mock(return_value={'state':'unchanged'});chain.marker=mock.Mock()
        revoked=False; calls=[]
        def revoke(statement):
            nonlocal revoked
            self.assertEqual(len(calls),3);self.assertIn("role_id='owned-viewer'",statement);revoked=True
        chain.sql=revoke
        def call(tier,method,path,**kwargs):
            self.assertEqual(kwargs['cookie'],'scoped-viewer-cookie')
            self.assertEqual((tier,method),('production','GET'))
            if not revoked:
                self.assertTrue(callable(kwargs['validate']));self.assertEqual(kwargs.get('want',200),200)
                value=dict(id='agent',scope=dict(application_id='app',environment_id='prod',owner_team_id=''),
                    version='v1',revision=6,routing_connected=False,release=dict(routing_connected=False,observed_traffic=None),
                    score=None,runs_7d=None,versions=[dict(version=v,routing_connected=False,traffic=None,score=None,success=None,tokens=None) for v in ('v1','v2')])
                if '/prompts/' in path:value=dict(id='prompt',version='v3',registered_agent={'id':'agent'},scope=dict(application_id='app',environment_id='prod'),
                    lines=[dict(text='body')],partial=True,runtime_usage=None,publication_status=None,evaluation_score=None)
                if '/evals/' in path:value=dict(id='eval',title='title',status='pending',meta='pending',baseline_version='v1',candidate_version='v2',
                    partial=True,missing=['evaluation_result'],**{k:None for k in ('dataset','metrics','radar','cases','datasets','evaluators','result_ref','finished_at')})
                kwargs['validate'](value)
            else:self.assertEqual((kwargs['want'],kwargs['scenario']),(404,'authorization'))
            calls.append(path);return {},{}
        chain.call=call;chain.revocation_tests()
        self.assertEqual(calls[:3],calls[3:]);self.assertEqual(len(calls),6)
        self.assertEqual(chain.marker.call_args.args[1]['viewer_normal_before_revoke'],3)

    def intent_chain(self, tmp):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.run_id='a'*32;chain.project_name='testlab-promotion-'+chain.run_id
        chain.backend={'owner':candidate.OWNER};chain.projects={};chain.tokens=[]
        chain.resource_intents=[];chain.work=Path(tmp)
        chain.state={'catalog_resource_intents':chain.resource_intents,'projects':chain.projects,'token_ids':chain.tokens}
        return chain

    def test_unknown_accepted_creation_is_recovered_readonly_from_durable_exact_intent(self):
        for kind in ('project','token'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as tmp:
                chain=self.intent_chain(tmp);accepted=False;posts=[]
                name=chain.project_name if kind=='project' else 'promotion-read-'+chain.run_id
                row=dict(id=42,name=name)
                if kind=='project':
                    row.update(path=name,path_with_namespace='root/'+name,namespace={'id':1},description=chain.db_marker('project-main'))
                    body=dict(name=name,path=name,description=row['description'])
                    path='/api/v4/projects'
                else:
                    row.update(user_id=3,scopes=['read_api'],expires_at='2026-10-07')
                    body={k:row[k] for k in ('name','scopes','expires_at')}
                    path='/api/v4/users/3/personal_access_tokens'
                def remote(method,api,body=None,**kwargs):
                    nonlocal accepted
                    if method=='POST':
                        intent=json.loads((chain.work/'state.json').read_text())['catalog_resource_intents'][0]
                        self.assertEqual(intent['status'],'pending');self.assertEqual(intent['name'],name)
                        self.assertEqual((chain.work/'state.json').stat().st_mode & 0o777,0o600)
                        if kind=='project':self.assertEqual(body['namespace_id'],1)
                        posts.append(api);accepted=True;raise OSError('accepted but response lost')
                    self.assertEqual(method,'GET')
                    if '/namespaces?' in api:return [dict(id=1,full_path='root',path='root',kind='user')],{}
                    if '/personal_access_tokens?' in api:return ([row] if accepted else []),{}
                    return (row if accepted else {},{},200 if accepted else 404)
                with mock.patch.object(candidate.promotion.Chain,'git',side_effect=remote):
                    try:
                        with self.assertRaises(OSError):chain.git('POST',path,body,want=201)
                    finally:self.assertEqual(chain.recover_intents(),[])
                self.assertEqual(posts,[path]);self.assertEqual(chain.resource_intents[0]['id'],42)
                if kind=='project':self.assertEqual(chain.projects['main']['id'],42)
                else:self.assertEqual(chain.tokens,[dict(id=42,name=name,user_id=3)])

    def test_foreign_or_absent_uncertain_intent_never_claims_complete_cleanup(self):
        for outcome in ('foreign','absent'):
            with self.subTest(outcome=outcome),tempfile.TemporaryDirectory() as tmp:
                chain=self.intent_chain(tmp)
                intent=dict(kind='project',key='main',name=chain.project_name,namespace_id=1,
                    full_path='root/'+chain.project_name,marker=chain.db_marker('project-main'),status='pending')
                chain.resource_intents.append(intent);chain.save()
                row=dict(id=42,name=chain.project_name,path=chain.project_name,path_with_namespace=intent['full_path'],
                         namespace={'id':1},description='foreign owner/run')
                response=(row,{},200) if outcome=='foreign' else ({},{},404)
                def known_cleanup(_):candidate.promotion.emit('promotion.cleanup','passed',{'known_resources_removed':True})
                with (mock.patch.object(candidate.promotion.Chain,'git',return_value=response) as remote,
                      mock.patch.object(candidate.promotion.Chain,'cleanup',new=known_cleanup),
                      mock.patch.object(candidate,'emit') as emit,
                      mock.patch.object(candidate.promotion,'emit',new=lambda *a,**kw:emit(*a,**kw))):
                    with self.assertRaises(candidate.Blocked):chain.cleanup()
                self.assertTrue(all(c.args[0]=='GET' for c in remote.call_args_list))
                self.assertEqual(chain.projects,{})
                self.assertEqual([(c.args[0],c.args[1]) for c in emit.call_args_list],
                                 [('promotion.cleanup','blocked'),('agent_catalog.cleanup','blocked')])

    def test_verified_token_save_failure_still_runs_original_cleanup_and_stays_blocked(self):
        with tempfile.TemporaryDirectory() as tmp:
            chain=self.intent_chain(tmp)
            chain.resource_intents.append(dict(kind='token',name='promotion-read-'+chain.run_id,
                user_id=3,expires_at='2026-10-07',status='handle-verified',id=42))
            chain.save=mock.Mock(side_effect=OSError('controlled persistence failure'))
            original_cleanup=mock.Mock(side_effect=lambda:candidate.promotion.emit('promotion.cleanup','passed',{}))
            with (mock.patch.object(candidate.promotion.Chain,'cleanup',new=lambda _:original_cleanup()),
                  mock.patch.object(candidate,'emit') as emit,
                  mock.patch.object(candidate.promotion,'emit',new=lambda *a,**kw:emit(*a,**kw))):
                with self.assertRaises(candidate.Blocked):chain.cleanup()
            original_cleanup.assert_called_once();chain.save.assert_called_once()
            self.assertEqual(chain.tokens,[dict(id=42,name='promotion-read-'+chain.run_id,user_id=3)])
            self.assertEqual([(c.args[0],c.args[1]) for c in emit.call_args_list],
                             [('promotion.cleanup','blocked'),('agent_catalog.cleanup','blocked')])

if __name__=='__main__':unittest.main()

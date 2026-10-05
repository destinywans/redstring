import contextlib
import copy
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_ai_preferences_audit_coverage as ai


class AIPreferenceAuditTests(unittest.TestCase):
    def fixture(self):
        actor,peer='a'*32,'b'*32
        current=dict(default={'model':'openai:readonly-synthetic'},canvas_inherit=True,show_reasoning='collapsed',
                     auto_context=True,memory=True,budget_warn_tokens=8000)
        catalog=dict(default=current['default'],items=[dict(id='openai:readonly-synthetic',api='chat_completions',
            available=True,reasoning=dict(kind='levels',levels=[dict(id='low',label='low')]))])
        payload=ai.payload_from_catalog(catalog,current)
        row=lambda uid:dict(user_id=uid,theme='system',locale='zh-CN',density='comfortable',reduce_motion=False,
            notification_settings={'quiet':{'enabled':False}},ai_preferences=copy.deepcopy(current),
            updated_at='2026-10-05T01:00:00+00:00')
        before=dict(settings=[row(actor),row(peer)],audit=[dict(id=1,action='existing.fixture')])
        after=copy.deepcopy(before)
        after['settings'][0].update(ai_preferences=copy.deepcopy(payload),updated_at='2026-10-05T01:00:01+00:00')
        after['audit'].append(dict(id=3,actor_id=actor,actor_kind='user',application_id='',action=ai.ACTION,
            target=actor,outcome='success',changes=dict(model=payload['default']['model'],reasoning=''),
            created_at='2026-10-05T01:00:01+00:00'))
        return actor,peer,current,catalog,payload,before,after

    def test_private_lifecycle_ports_do_not_mutate_shared_module(self):
        self.assertIsNot(ai.ro,shared)
        self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(ai.ro.PORTS,(18746,18747))

    def test_inherited_default_catalog_uses_exact_chat_completions_protocol(self):
        _,_,current,catalog,*_=self.fixture()
        # ro.start does not set OPENAI_API; command_env excludes it from ambient
        # input; inference.New defaults the configured OpenAI provider to this
        # protocol. The formal catalog's api field is the actual Model.API.
        catalog['items'][0]['api']='chat_completions'
        self.assertEqual(ai.payload_from_catalog(catalog,current)['default'],current['default'])

    def test_inherited_protocol_is_derived_from_guarded_configuration_not_any_supported_api(self):
        source=(ai.ROOT/'tools/testlab/api_readonly_chain.py').read_text()
        start=source[source.index('    def start(self):'):source.index('    def seed(self):')]
        self.assertIn("OPENAI_MODEL='readonly-synthetic'",start)
        self.assertNotIn('OPENAI_API=',start)
        self.assertNotIn('OPENAI_API',ai.ro.command_env.__globals__['ENV_ALLOWLIST'])
        runtime=(ai.ROOT/'internal/inference/runtime.go').read_text()
        default=runtime[runtime.index('if p.API == ""'):runtime.index('if !((p.Kind == "openai"')]
        self.assertIn('p.API = "chat_completions"',default)
        dto=(ai.ROOT/'internal/v3settings/ai.go').read_text()
        self.assertIn('"api": m.API',dto)
        _,_,current,catalog,*_=self.fixture()
        for unsupported_here in ('responses','messages','ollama','',None):
            altered=copy.deepcopy(catalog);altered['items'][0]['api']=unsupported_here
            with self.subTest(protocol=unsupported_here),self.assertRaises(AssertionError):
                ai.payload_from_catalog(altered,current)

    def test_opt_in_precedes_lease_doctor_and_resources(self):
        with (mock.patch.object(sys,'argv',['ai']),mock.patch.object(ai,'doctor') as doctor,
              mock.patch.object(ai.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):ai.main()
        doctor.assert_not_called();opened.assert_not_called()

    def test_inventory_rejects_model_probe_inference_external_and_other_mutations(self):
        contract=ai.Contract(ai.ROOT)
        for method,path in (*ai.TARGETS,*ai.SUPPORT):contract.canonical(method,'/api/v1'+path)
        for method,path in [('POST','/api/v1/models/test'),('POST','/api/v1/conversations'),
            ('PUT','/api/v1/models/admin'),('PUT','/api/v1/me/notification-settings'),
            ('GET','http://127.0.0.1:9/v1/models'),('GET','/api/v1/models?probe=true')]:
            with self.assertRaises((ai.Blocked,AssertionError)):contract.canonical(method,path)

    def test_frozen_identity_cpu_memory_fail_closed(self):
        data=dict(source_tree_sha=ai.SOURCE_SHA,binary_sha256=ai.BINARY_SHA,runtime_sha=ai.RUNTIME_SHA)
        ai.frozen_identity(data,ai.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError):ai.frozen_identity(dict(data,**{key:'0'*64}),ai.PG_CID)
        with self.assertRaises(AssertionError):ai.frozen_identity(data,'other')
        info=dict(Id=ai.PG_CID,HostConfig=dict(NanoCpus=4_000_000_000,Memory=2<<30))
        with mock.patch.object(ai,'_inspect_pg',return_value=info):ai.inspect_pg('owned')
        for bad in [dict(info,Id='other'),dict(info,HostConfig={}),
            dict(info,HostConfig=dict(NanoCpus=4_000_000_000,Memory=3<<30))]:
            with mock.patch.object(ai,'_inspect_pg',return_value=bad):
                with self.assertRaises(AssertionError):ai.inspect_pg('owned')

    def test_source_drift_prevents_http(self):
        chain=ai.Chain.__new__(ai.Chain);chain.frozen=mock.Mock(side_effect=ai.Blocked('drift'))
        with mock.patch.object(ai.ro.Chain,'call') as call:
            with self.assertRaises(ai.Blocked):chain.call('reader','PUT','/api/v1/me/ai-preferences')
        call.assert_not_called()

    def test_formal_source_has_same_transaction_and_audits_each_successful_put(self):
        source=(ai.ROOT/'internal/v3settings/ai.go').read_text()
        put=source[source.index('func (s *Service) putAI'):source.index('// ModelsHandler')]
        self.assertIn('defer tx.Rollback(ctx)',put)
        self.assertIn('updated_at=now()',put)
        self.assertIn("'v3.ai_preferences.updated'",put)
        self.assertIn("'success'",put)
        self.assertIn('map[string]any{"model": p.Default.Model, "reasoning": p.Default.Reasoning}',put)
        self.assertLess(put.index('INSERT INTO v3_user_settings'),put.index('INSERT INTO management_audit'))
        self.assertLess(put.index('INSERT INTO management_audit'),put.index('tx.Commit'))
        self.assertNotIn('Idempotency',put)
        self.assertNotIn('IS DISTINCT FROM',put)
        self.assertIn('"settings_unavailable"',(ai.ROOT/'internal/v3settings/service.go').read_text())
        self.assertIn('user_id text PRIMARY KEY',(ai.ROOT/'migrations/026_v3_personal_settings.sql').read_text())

    def test_catalog_payload_uses_advertised_default_without_mutating_it(self):
        _,_,current,catalog,payload,*_=self.fixture()
        self.assertEqual(payload['default'],catalog['default'])
        self.assertNotEqual(payload,current)
        self.assertEqual(current['show_reasoning'],'collapsed')
        for bad in [dict(catalog,items=[]),dict(catalog,default={'model':'unadvertised'}),
                    dict(catalog,items=[dict(catalog['items'][0],id='unadvertised')]),
                    dict(catalog,items=[dict(catalog['items'][0],api='ollama')])]:
            with self.assertRaises(AssertionError):ai.payload_from_catalog(bad,current)

    def test_preferences_dto_rejects_partial_bool_as_int_and_unadvertised_reasoning(self):
        _,_,current,catalog,*_=self.fixture()
        for bad in [dict(current,memory=1),dict(current,budget_warn_tokens=True),
                    dict(current,default={}),dict(current,unexpected=True)]:
            with self.assertRaises(AssertionError):ai.verify_preferences(bad)
        altered=dict(current,default=dict(current['default'],reasoning='unadvertised'))
        with self.assertRaises(AssertionError):ai.payload_from_catalog(dict(catalog,default=altered['default']),altered)

    def test_fault_is_exact_nonce_actor_target_action_not_nonexistent_environment_change(self):
        name,create,drop=ai.fault_statements('a'*32,'b'*32)
        for clause in ["NEW.action='v3.ai_preferences.updated'","NEW.actor_id='"+'b'*32+"'","NEW.target='"+'b'*32+"'"]:
            self.assertIn(clause,create)
        self.assertNotIn('environment_id',create)
        self.assertIn('DROP TRIGGER IF EXISTS '+name+' ON public.management_audit',drop)
        self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()',drop)
        with self.assertRaises(AssertionError):ai.fault_statements('a'*32,"x'; DROP TABLE users;--")

    def test_rollback_rejects_partial_value_timestamp_other_owner_or_audit_mutations(self):
        actor,peer,current,catalog,payload,before,after=self.fixture()
        ai.verify_rollback(before,copy.deepcopy(before))
        for field,value in [('ai_preferences',payload),('updated_at','2026-10-05T02:00:00+00:00'),('theme','light')]:
            changed=copy.deepcopy(before);changed['settings'][0][field]=value
            with self.assertRaises(AssertionError):ai.verify_rollback(before,changed)
        changed=copy.deepcopy(before);changed['audit'].append({'id':2,'outcome':'failed'})
        with self.assertRaises(AssertionError):ai.verify_rollback(before,changed)
        changed=copy.deepcopy(before);changed['settings'].pop()
        with self.assertRaises(AssertionError):ai.verify_rollback(before,changed)

    def test_recovery_preserves_complete_unrelated_fields_and_requires_exact_success_audit(self):
        actor,_,_,_,payload,before,after=self.fixture()
        ai.verify_update(before,after,actor,payload)
        for field,value in [('outcome','succeeded'),('actor_id','d'*32),('target','d'*32),('action','wrong'),
                            ('changes',{'model':'wrong','reasoning':''})]:
            bad=copy.deepcopy(after);bad['audit'][-1][field]=value
            with self.assertRaises(AssertionError):ai.verify_update(before,bad,actor,payload)
        for field,value in [('theme','light'),('notification_settings',{}),('ai_preferences',{}),
                            ('updated_at',before['settings'][0]['updated_at'])]:
            bad=copy.deepcopy(after);bad['settings'][0][field]=value
            with self.assertRaises(AssertionError):ai.verify_update(before,bad,actor,payload)
        bad=copy.deepcopy(after);bad['settings'][1]['updated_at']='2026-10-05T03:00:00+00:00'
        with self.assertRaises(AssertionError):ai.verify_update(before,bad,actor,payload)
        bad=copy.deepcopy(after);bad['audit'].append(copy.deepcopy(bad['audit'][-1]))
        with self.assertRaises(AssertionError):ai.verify_update(before,bad,actor,payload)

    def test_repeated_identical_state_still_requires_timestamp_and_one_new_audit(self):
        actor,_,_,_,payload,before,after=self.fixture()
        repeated=copy.deepcopy(after)
        repeated['settings'][0]['updated_at']='2026-10-05T01:00:02+00:00'
        repeated['audit'].append(dict(repeated['audit'][-1],id=5,created_at='2026-10-05T01:00:02+00:00'))
        ai.verify_update(after,repeated,actor,payload)
        self.assertEqual(after['settings'][0]['ai_preferences'],repeated['settings'][0]['ai_preferences'])
        no_audit=copy.deepcopy(repeated);no_audit['audit']=copy.deepcopy(after['audit'])
        with self.assertRaises(AssertionError):ai.verify_update(after,no_audit,actor,payload)
        backwards=copy.deepcopy(repeated);backwards['settings'][0]['updated_at']='2026-10-05T00:59:59+00:00'
        with self.assertRaises(AssertionError):ai.verify_update(after,backwards,actor,payload)

    def test_ledger_is_whole_rows_all_users_all_audits_read_only(self):
        chain=ai.Chain.__new__(ai.Chain);chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.sql=mock.Mock(return_value='{}')
        chain.ledger();query=chain.sql.call_args.args[0]
        self.assertTrue(chain.sql.call_args.kwargs['read'])
        self.assertNotIn('WHERE',query)
        self.assertIn('json_agg(to_jsonb(t) ORDER BY user_id)',query)
        self.assertIn('json_agg(to_jsonb(t) ORDER BY id)',query)

    def test_restore_uses_shared_four_second_deadline_and_verifies_absence(self):
        chain=ai.Chain.__new__(ai.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32};chain.fault_attempted=True
        chain.database='owned-db';chain.db_user='owned-role';chain.owner=mock.Mock()
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        with mock.patch.object(ai.time,'monotonic',return_value=100):chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual([x.kwargs['deadline'] for x in chain.pg.call_args_list],[104,104])
        self.assertFalse(chain.fault_attempted)

    def test_unknown_installation_outcome_still_attempts_restore(self):
        chain=ai.Chain.__new__(ai.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32};chain.fault_attempted=False
        chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.sql=mock.Mock(side_effect=ai.Blocked('unknown outcome'))
        with self.assertRaises(ai.Blocked):chain.install_fault()
        self.assertTrue(chain.fault_attempted)

    def test_cleanup_always_runs_parent_when_restore_fails(self):
        chain=ai.Chain.__new__(ai.Chain);chain.remove_fault=mock.Mock(side_effect=RuntimeError('restore failed'))
        with (mock.patch.object(ai.ro.Chain,'cleanup') as cleanup,mock.patch.object(ai.socket,'socket') as socket):
            socket.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaises(RuntimeError):chain.cleanup()
        cleanup.assert_called_once()

    def test_outer_execute_cleans_despite_business_and_reporting_failures(self):
        chain=mock.Mock();chain.run.side_effect=RuntimeError('business failed');chain.first_red.side_effect=OSError('report failed')
        with mock.patch.object(ai.ro,'emit',side_effect=OSError('stdout failed')):
            with self.assertRaises(RuntimeError):ai.ro.execute(chain)
        chain.cleanup.assert_called_once()

    def test_partial_rollback_restores_fault_and_stops_before_recovery(self):
        actor,peer,current,catalog,payload,before,after=self.fixture()
        chain=ai.Chain.__new__(ai.Chain);chain.users={'reader':actor,'peer':peer}
        for name in ('provision','start','seed','login','read','marker_case','install_fault','remove_fault'):
            setattr(chain,name,mock.Mock())
        chain.call=mock.Mock(side_effect=[(current,{}),(catalog,{}),(current,{}),(current,{}),({}, {})])
        chain.ledger=mock.Mock(side_effect=[before,after])
        with self.assertRaises(AssertionError):chain.run()
        chain.remove_fault.assert_called_once()
        self.assertEqual(chain.call.call_count,5)
        self.assertEqual(chain.call.call_args.kwargs['want'],503)


if __name__=='__main__':unittest.main()

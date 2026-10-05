import copy
import contextlib
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_user_settings_audit_coverage as settings

class SettingsAuditTests(unittest.TestCase):
    def fixture(self, kind='preferences'):
        actor,peer='a'*32,'b'*32
        prefs=dict(theme='light',locale='en-US',density='compact',reduce_motion=True)
        notify=dict(quiet={'enabled':False,'from':'22:00','to':'08:00'},digest='daily',subscriptions=[])
        row=lambda uid:dict(user_id=uid,**prefs,notification_settings=copy.deepcopy(notify),
            ai_preferences={'memory':True},profile_notifications={'email':False},
            updated_at='2026-10-05T01:00:00+00:00',created_at='2026-10-05T00:00:00+00:00')
        before={'settings':[row(actor),row(peer)],'audit':[{'id':1,'action':'earlier'}]}
        after=copy.deepcopy(before)
        if kind=='preferences':
            patch={'theme':'dark'};expected=dict(prefs,theme='dark')
            after['settings'][0]['theme']='dark'
            changes={k:patch.get(k) for k in settings.PREF_KEYS}
        else:
            patch={'quiet':True};expected=copy.deepcopy(notify);expected['quiet']['enabled']=True
            after['settings'][0]['notification_settings']=copy.deepcopy(expected)
            changes={'quiet':True,'digest':'daily','subscriptions':0}
        after['settings'][0]['updated_at']='2026-10-05T01:00:01+00:00'
        after['audit'].append(dict(id=3,actor_id=actor,actor_kind='user',application_id='',target=actor,
            action=settings.ACTIONS[kind],outcome='success',changes=changes))
        return actor,peer,before,after,expected,patch

    def test_exact_success_for_both_actions(self):
        for kind in settings.ACTIONS:
            actor,_,before,after,expected,patch=self.fixture(kind)
            settings.verify_update(before,after,actor,kind,expected,patch)

    def test_detects_partial_row_and_other_owner_corruption(self):
        for kind in settings.ACTIONS:
            actor,_,before,after,expected,patch=self.fixture(kind)
            for row_index,field,bad in [(0,'ai_preferences',{}),(0,'profile_notifications',{}),
                    (0,'created_at','later'),(1,'locale','zh-CN')]:
                altered=copy.deepcopy(after);altered['settings'][row_index][field]=bad
                with self.subTest(kind=kind,field=field),self.assertRaises(AssertionError):
                    settings.verify_update(before,altered,actor,kind,expected,patch)

    def test_timestamp_must_advance(self):
        actor,_,before,after,expected,patch=self.fixture()
        after['settings'][0]['updated_at']=before['settings'][0]['updated_at']
        with self.assertRaises(AssertionError):settings.verify_update(before,after,actor,'preferences',expected,patch)

    def test_exact_audit_action_outcome_patch_and_actor(self):
        for kind in settings.ACTIONS:
            actor,_,before,after,expected,patch=self.fixture(kind)
            for field,bad in [('action','other'),('actor_id','other'),('target','other'),('outcome','passed'),('changes',{})]:
                altered=copy.deepcopy(after);altered['audit'][-1][field]=bad
                with self.subTest(kind=kind,field=field),self.assertRaises(AssertionError):
                    settings.verify_update(before,altered,actor,kind,expected,patch)

    def test_prior_audit_and_exact_one_new_preserved(self):
        actor,_,before,after,expected,patch=self.fixture()
        for audit in [after['audit'][1:],after['audit']+[dict(after['audit'][-1],id=4)]]:
            with self.assertRaises(AssertionError):settings.verify_update(before,dict(after,audit=audit),actor,'preferences',expected,patch)

    def test_rollback_detects_full_rows_timestamps_and_audit(self):
        _,_,before,after,_,_=self.fixture()
        settings.verify_rollback(before,copy.deepcopy(before))
        with self.assertRaises(AssertionError):settings.verify_rollback(before,after)

    def test_repeated_state_requires_another_audit_and_timestamp(self):
        for kind in settings.ACTIONS:
            actor,_,_,after,expected,patch=self.fixture(kind)
            repeated=copy.deepcopy(after);repeated['settings'][0]['updated_at']='2026-10-05T01:00:02+00:00'
            repeated['audit'].append(dict(repeated['audit'][-1],id=5))
            settings.verify_update(after,repeated,actor,kind,expected,patch)
            repeated['audit'].pop()
            with self.assertRaises(AssertionError):settings.verify_update(after,repeated,actor,kind,expected,patch)

    def test_route_inventory_excludes_ai_and_fabricated_preference_GET_or_PUT(self):
        c=settings.Contract(settings.ROOT)
        for method,path in (*settings.TARGETS,*settings.SUPPORT):c.canonical(method,'/api/v1'+path)
        for method,path in [('GET','/api/v1/me/preferences'),('PUT','/api/v1/me/preferences'),
                ('PUT','/api/v1/me/ai-preferences'),('GET','/api/v1/models'),
                ('GET','http://example.invalid/api/v1/me/notification-settings'),
                ('GET','/api/v1/me/notification-settings?probe=true')]:
            with self.assertRaises((settings.Blocked,AssertionError)):c.canonical(method,path)

    def test_private_ports_and_marker_preserve_inherited_instance_string(self):
        import api_readonly_chain as shared
        self.assertIsNot(shared,settings.ro)
        self.assertEqual(shared.PORTS,(18602,18603));self.assertEqual(settings.ro.PORTS,(18748,18749))
        self.assertNotIn('marker',settings.Chain.__dict__)
        self.assertEqual(len(settings.REQUIRED_CASES),13)

    def test_opt_in_precedes_resources(self):
        with (mock.patch.object(sys,'argv',['settings']),mock.patch.object(settings,'doctor') as doctor,
              mock.patch.object(settings.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):settings.main()
        doctor.assert_not_called();opened.assert_not_called()

    def test_frozen_identity_and_exact_PG_cpu_memory(self):
        data=dict(source_tree_sha=settings.SOURCE_SHA,binary_sha256=settings.BINARY_SHA,runtime_sha=settings.RUNTIME_SHA)
        settings.frozen_identity(data,settings.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError):settings.frozen_identity(dict(data,**{key:'drift'}),settings.PG_CID)
        info=dict(Id=settings.PG_CID,HostConfig=dict(NanoCpus=4_000_000_000,Memory=2<<30))
        with mock.patch.object(settings,'_inspect_pg',return_value=info):settings.inspect_pg('owned')
        for bad in [dict(info,Id='other'),dict(info,HostConfig={})]:
            with mock.patch.object(settings,'_inspect_pg',return_value=bad):
                with self.assertRaises(AssertionError):settings.inspect_pg('owned')

    def test_source_drift_prevents_HTTP(self):
        chain=settings.Chain.__new__(settings.Chain);chain.frozen=mock.Mock(side_effect=settings.Blocked('drift'))
        with mock.patch.object(settings.ro.Chain,'call') as call:
            with self.assertRaises(settings.Blocked):chain.call('reader','PATCH','/api/v1/me/preferences')
        call.assert_not_called()

    def test_source_contract_both_audits_are_transactional_and_per_success(self):
        s=(settings.ROOT/'internal/v3settings/service.go').read_text()
        a=(settings.ROOT/'internal/v3settings/ai.go').read_text()
        prefs=s[s.index('func (s *Service) savePrefs'):s.index('func decodePreferencesPatch')]
        notification=a[a.index('func (s *Service) putNotifications'):a.index('func (s *Service) registerAI')]
        for body,action,write in [(prefs,settings.ACTIONS['preferences'],'INSERT INTO v3_user_settings'),
                (notification,settings.ACTIONS['notifications'],'UPDATE v3_user_settings')]:
            self.assertIn('defer tx.Rollback(ctx)',body);self.assertIn(action,body)
            self.assertIn("'success'",body);self.assertIn('updated_at=now()',body)
            self.assertLess(body.index(write),body.index('INSERT INTO management_audit'))
            self.assertLess(body.index('INSERT INTO management_audit'),body.index('tx.Commit'))
            self.assertNotIn('Idempotency',body);self.assertNotIn('IS DISTINCT FROM',body)
        self.assertIn('COALESCE($3,v3_user_settings.locale)',prefs)
        self.assertIn('FOR UPDATE',notification)
        self.assertIn('json.Unmarshal(b, &v.Quiet.Enabled)',notification)
        self.assertIn('"PATCH /api/v1/me/preferences"',s)
        self.assertIn('"PUT /api/v1/me/notification-settings"',a)
        self.assertIn('"settings_unavailable"',s)

    def test_fault_exact_action_actor_target_and_not_environment(self):
        for kind in settings.ACTIONS:
            name,create,drop=settings.fault_statements('a'*32,'b'*32,kind)
            self.assertIn(settings.ACTIONS[kind],create);self.assertIn("NEW.actor_id='"+'b'*32+"'",create)
            self.assertIn("NEW.target='"+'b'*32+"'",create);self.assertNotIn('environment_id',create)
            self.assertIn('DROP TRIGGER IF EXISTS '+name,drop)
        with self.assertRaises(AssertionError):settings.fault_statements('a'*32,'b'*32,'AI')

    def test_whole_ledger_is_readonly_without_filter(self):
        chain=settings.Chain.__new__(settings.Chain);chain.frozen=mock.Mock();chain.owner=mock.Mock()
        chain.sql=mock.Mock(return_value='{}');chain.ledger();query=chain.sql.call_args.args[0]
        self.assertNotIn('WHERE',query);self.assertIn('to_jsonb(t)',query);self.assertTrue(chain.sql.call_args.kwargs['read'])

    def test_restore_uses_one_four_second_deadline(self):
        chain=settings.Chain.__new__(settings.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32}
        chain.fault_attempted=True;chain.fault_kind='notifications';chain.database='owned';chain.db_user='owned'
        chain.owner=mock.Mock();chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        with mock.patch.object(settings.time,'monotonic',return_value=100):chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual([c.kwargs['deadline'] for c in chain.pg.call_args_list],[104,104])
        self.assertFalse(chain.fault_attempted)

    def test_unknown_install_outcome_keeps_cleanup_armed(self):
        chain=settings.Chain.__new__(settings.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32}
        chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.fault_attempted=False
        chain.sql=mock.Mock(side_effect=settings.Blocked('unknown outcome'))
        with self.assertRaises(settings.Blocked):chain.install_fault('preferences')
        self.assertTrue(chain.fault_attempted);self.assertEqual(chain.fault_kind,'preferences')

    def test_partial_rollback_always_restores_and_stops_before_recovery(self):
        for kind in settings.ACTIONS:
            actor,peer,before,after,expected,patch=self.fixture(kind)
            chain=settings.Chain.__new__(settings.Chain);chain.users={'reader':actor,'peer':peer}
            for name in ('install_fault','remove_fault','marker_case','notifications_read'):setattr(chain,name,mock.Mock())
            chain.call=mock.Mock(return_value=({},{}));chain.ledger=mock.Mock(side_effect=[before,after])
            with self.assertRaises(AssertionError):chain.exercise(kind,'PATCH' if kind=='preferences' else 'PUT',
                '/api/v1/me/preferences' if kind=='preferences' else '/api/v1/me/notification-settings',{},patch,expected,{})
            chain.remove_fault.assert_called_once();self.assertEqual(chain.call.call_count,1)
            self.assertEqual(chain.call.call_args.kwargs['want'],503)
            self.assertEqual(chain.call.call_args.kwargs['code'],'settings_unavailable')

    def test_cleanup_runs_parent_despite_restore_failure(self):
        chain=settings.Chain.__new__(settings.Chain);chain.remove_fault=mock.Mock(side_effect=RuntimeError('restore'))
        with (mock.patch.object(settings.ro.Chain,'cleanup') as cleanup,mock.patch.object(settings.socket,'socket') as sock):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaises(RuntimeError):chain.cleanup()
        cleanup.assert_called_once()

    def test_outer_cleanup_survives_business_and_reporting_failure(self):
        chain=mock.Mock();chain.run.side_effect=RuntimeError('business');chain.first_red.side_effect=OSError('report')
        with mock.patch.object(settings.ro,'emit',side_effect=OSError('stdout')):
            with self.assertRaises(RuntimeError):settings.ro.execute(chain)
        chain.cleanup.assert_called_once()

if __name__=='__main__':unittest.main()

import contextlib
import copy
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_incident_fault_interfaces_coverage as shared_incident
import api_postmortem_audit_rollback_coverage as pm

class PostmortemAuditTests(unittest.TestCase):
    def identity(self):
        return dict(nonce='a'*32,actor='b'*32,app='c'*32,case='INC-'+'d'*32,revision='case:9')

    def test_private_helpers_and_explicit_no_full_chain_inventory(self):
        self.assertIsNot(pm.ro,shared);self.assertIsNot(pm.incident,shared_incident)
        self.assertEqual(shared.PORTS,(18602,18603));self.assertEqual(shared_incident.ro.PORTS,(18740,18741))
        self.assertEqual(pm.ro.PORTS,(18744,18745))
        contract=pm.Contract(pm.ROOT);case=self.identity()['case']
        for verb,path in [('POST','/postmortems'),('POST','/postmortems/'+'e'*32+'/publish'),
                          ('GET','/postmortems/draft?incident='+case),('GET','/postmortems/records?tab=review')]:
            contract.canonical(verb,'/api/v1'+path)
        for verb,path in [('POST','/canvases'),('POST','/runbooks'),('POST','/uploads'),
                          ('GET','/postmortems/draft'),('GET','/postmortems/records?tab=all'),
                          ('GET','/postmortems/draft?incident='+case+'&incident='+case),
                          ('POST','/postmortems?q=1')]:
            with self.assertRaises((AssertionError,pm.Blocked)):contract.canonical(verb,'/api/v1'+path)

    def test_explicit_live_required_before_lease_and_doctor(self):
        with (mock.patch.object(sys,'argv',['pm']),mock.patch.object(pm.os,'open') as opened,
              mock.patch.object(pm,'doctor') as doctor,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):pm.main()
        opened.assert_not_called();doctor.assert_not_called()

    def test_two_exact_native_audit_faults_and_uuid_ddl(self):
        i=self.identity()
        for action,target in [('postmortem.created',None),('postmortem.published','e'*32)]:
            name,create,drop=pm.fault_statements(**i,action=action,target=target)
            self.assertIn("NEW.action='"+action+"'",create)
            for field,value in [('actor_id',i['actor']),('application_id',i['app'])]:
                self.assertIn('NEW.'+field+"='"+value+"'",create)
            self.assertIn("NEW.changes->>'incident_id'='"+i['case']+"'",create)
            self.assertIn("NEW.changes->>'source_revision'='case:9'",create)
            self.assertIn("NEW.changes->>'environment_id'='test'",create)
            if target:self.assertIn("NEW.target='"+target+"'",create)
            self.assertTrue(create.startswith('BEGIN;'));self.assertTrue(create.endswith('COMMIT;'))
            self.assertIn('DROP TRIGGER IF EXISTS '+name,drop)
            self.assertEqual(drop,pm.drop_statement(i['nonce']))
        for changed in [dict(actor='human'),dict(app='app'),dict(case='INC-other'),dict(revision='ops:9')]:
            with self.assertRaises(AssertionError):pm.fault_statements(**dict(i,**changed),action='postmortem.created')
        with self.assertRaises(AssertionError):pm.fault_statements(**i,action='unknown')
        with self.assertRaises(AssertionError):pm.fault_statements(**i,action='postmortem.published')

    def fixture(self):
        i=self.identity();uid='e'*32;actor=i['actor'];reviewer='f'*32
        body=dict(incident_id=i['case'],source_revision=i['revision'],status='review',
                  sections=[dict(key='root',title='Root cause',text='UUID root')],
                  actions=[dict(text='UUID action',owner=actor,due='2099-01-01')])
        before=dict(postmortems=[],cases=[dict(id=i['case'],status='resolved',status_revision=2,
                    resolved_status_event_id=9,investigation_id=None,resolved_at='2026-10-05T00:00:00Z')],links=[dict(case_id=i['case'])],
                    status_events=[dict(id=9,case_id=i['case'],revision=2,from_status='active',to_status='resolved',actor_id=actor)],
                    timeline=[],alerts=[dict(id='alert')],audit=[dict(id=1,action='incident.create')],
                    investigations=0,events=0)
        review=copy.deepcopy(before)
        review['postmortems']=[dict(id=uid,incident_id=i['case'],created_by=actor,application_id=i['app'],
            environment_id='test',status='review',sections=body['sections'],actions=body['actions'],
            source_kind='case',source_case_status_event_id=9,close_event_seq=None,investigation_id=None,
            created_at='2026-10-05T00:00:00+00:00',updated_at='2026-10-05T00:00:00+00:00')]
        changes=dict(incident_id=i['case'],environment_id='test',source_kind='case',source_revision=i['revision'])
        review['audit'].append(dict(id=2,action='postmortem.created',actor_id=actor,actor_kind='user',
            application_id=i['app'],target=uid,outcome='succeeded',changes=dict(changes,status='review')))
        published=copy.deepcopy(review);published['postmortems'][0].update(status='published',updated_at='2026-10-05T00:00:01+00:00')
        published['audit'].append(dict(id=3,action='postmortem.published',actor_id=reviewer,actor_kind='user',
            application_id=i['app'],target=uid,outcome='succeeded',changes=changes))
        return i,body,before,review,published

    def test_create_body_revision_actor_and_single_audit_are_not_optional(self):
        i,body,before,review,_=self.fixture();made={'id':'e'*32}
        def verify(value):pm.verify_create(before,value,made,body,i['actor'],i['app'],9)
        verify(review)
        for field,value in [('sections',[]),('actions',[]),('source_case_status_event_id',8),
                            ('status','published'),('created_by','f'*32),('updated_at','changed')]:
            bad=copy.deepcopy(review);bad['postmortems'][0][field]=value
            with self.assertRaises(AssertionError):verify(bad)
        for key,value in [('status_events',[]),('audit',before['audit']),('cases',[]),('investigations',1)]:
            with self.assertRaises(AssertionError):verify(dict(review,**{key:value}))
        bad=copy.deepcopy(review);bad['audit'][-1]['changes']['source_revision']='case:8'
        with self.assertRaises(AssertionError):verify(bad)

    def test_publish_can_only_change_status_time_and_append_independent_audit(self):
        i,_,_,review,published=self.fixture();result=dict(id='e'*32,status='published')
        def verify(value):pm.verify_publish(review,value,result,'f'*32,i['app'],i['case'],i['revision'])
        verify(published)
        for field,value in [('sections',[]),('actions',[]),('source_case_status_event_id',8),
                            ('created_by','f'*32),('created_at','changed'),('status','review'),
                            ('updated_at','2026-10-04T00:00:00+00:00')]:
            bad=copy.deepcopy(published);bad['postmortems'][0][field]=value
            with self.assertRaises(AssertionError):verify(bad)
        bad=copy.deepcopy(published);bad['audit'][-1]['actor_id']=i['actor']
        with self.assertRaises(AssertionError):verify(bad)
        bad=copy.deepcopy(published);bad['audit'].append(bad['audit'][-1])
        with self.assertRaises(AssertionError):verify(bad)
        with self.assertRaises(AssertionError):pm.verify_publish(review,published,result,i['actor'],i['app'],i['case'],i['revision'])

    def bare(self):
        chain=pm.Chain.__new__(pm.Chain);i,body,before,review,published=self.fixture()
        chain.nonce=i['nonce'];chain.app=i['app'];chain.case=i['case'];chain.revision=i['revision'];chain.event=9
        chain.users=dict(reader=i['actor'],peer='f'*32);chain.body=body;chain.marker='UUID title'
        chain.database='aiops_testlab_readonly_'+i['nonce'];chain.db_user='readonly_'+i['nonce'];chain.owner_tag='owned'
        chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.marker_case=mock.Mock()
        chain.install_fault=mock.Mock();chain.remove_fault=mock.Mock();chain.readback=mock.Mock()
        chain.ledger=mock.Mock(side_effect=[before,before,review,review,review,published])
        chain.call=mock.Mock(side_effect=[({},{}),({'id':'e'*32},{}),({},{}),({},{}),({'id':'e'*32,'status':'published'}, {})])
        return chain,before,review,published

    def test_success_flow_keeps_both_original_faults_and_distinct_recovery_instances(self):
        chain,_,_,_=self.bare();chain.writes()
        self.assertEqual([c.args for c in chain.install_fault.call_args_list],
                         [('postmortem.created','reader'),('postmortem.published','peer')])
        self.assertEqual(chain.remove_fault.call_count,2)
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],
                         ['create_rollback','create_fault_removed','create_recovery','independent_review',
                          'publish_rollback','publish_fault_removed','publish_recovery'])
        self.assertEqual(chain.call.call_args_list[0].kwargs['want'],503)
        self.assertEqual(chain.call.call_args_list[3].kwargs['want'],503)
        self.assertEqual(chain.call.call_args_list[1].kwargs['index'],1)
        self.assertEqual(chain.call.call_args_list[4].kwargs['index'],0)
        self.assertEqual(chain.readback.call_args_list[0].kwargs['index'],0)
        self.assertEqual(chain.readback.call_args_list[1].kwargs['index'],1)
        self.assertIsInstance(chain.marker,str)

    def test_create_first_red_restores_and_never_retries_or_claims_pass(self):
        chain,_,_,_=self.bare();chain.call.side_effect=AssertionError('first native red')
        with self.assertRaisesRegex(AssertionError,'first native red'):chain.writes()
        chain.remove_fault.assert_called_once();self.assertEqual(chain.call.call_count,1)
        chain.marker_case.assert_not_called()

    def test_changed_cas_source_fails_before_any_fault_or_postmortem_write(self):
        chain,before,_,_=self.bare();before['cases'][0]['resolved_status_event_id']=8
        chain.ledger.side_effect=None;chain.ledger.return_value=before
        with self.assertRaisesRegex(AssertionError,'persisted HTTP case/CAS close lineage'):chain.writes()
        chain.install_fault.assert_not_called();chain.call.assert_not_called();chain.marker_case.assert_not_called()

    def test_publish_failure_rolls_back_entire_review_including_updated_at(self):
        chain,before,review,published=self.bare()
        changed=copy.deepcopy(review);changed['postmortems'][0]['updated_at']='2026-10-05T00:00:01+00:00'
        chain.ledger.side_effect=[before,before,review,review,changed]
        with self.assertRaisesRegex(AssertionError,'published audit failure changed'):chain.writes()
        self.assertEqual(chain.remove_fault.call_count,2);self.assertEqual(chain.call.call_count,4)
        self.assertNotIn('publish_rollback',[c.args[0] for c in chain.marker_case.call_args_list])
        self.assertNotIn('publish_recovery',[c.args[0] for c in chain.marker_case.call_args_list])

    def test_restore_steps_share_four_second_deadline_without_source_frozen_dependency(self):
        chain,_,_,_=self.bare();chain.fault_attempted=True
        chain.remove_fault=lambda:pm.Chain.remove_fault(chain)
        chain.frozen.side_effect=pm.Blocked('source drift')
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        with mock.patch.object(pm.time,'monotonic',return_value=100):chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual(chain.pg.call_count,2)
        for call in chain.pg.call_args_list:
            self.assertEqual(call.args[1:],(chain.database,chain.db_user));self.assertEqual(call.kwargs,dict(deadline=104))
        self.assertTrue(chain.pg.call_args_list[1].args[0].startswith('BEGIN READ ONLY; '))
        chain.frozen.assert_not_called();self.assertFalse(chain.fault_attempted)

    def test_restore_residual_retains_pending_flag_and_blocks_false_success(self):
        chain,_,_,_=self.bare();chain.fault_attempted=True
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=True))])
        with self.assertRaisesRegex(AssertionError,'own PM audit fault remains'):pm.Chain.remove_fault(chain)
        self.assertTrue(chain.fault_attempted)

    def test_expired_four_second_budget_stops_before_native_pg_inspection(self):
        chain,_,_,_=self.bare();chain.fault_attempted=True
        chain.owner=lambda deadline:pm.incident.Chain.owner(chain,deadline=deadline)
        with (mock.patch.object(pm.time,'monotonic',side_effect=[100,105]),
              mock.patch.object(pm.ro,'inspect_pg') as inspect,mock.patch.object(pm.ro.subprocess,'run') as native):
            with self.assertRaisesRegex(pm.Blocked,'cleanup exhausted'):pm.Chain.remove_fault(chain)
        inspect.assert_not_called();native.assert_not_called();self.assertTrue(chain.fault_attempted)

    def test_install_unknown_outcome_retains_pending_restore(self):
        chain,_,_,_=self.bare();chain.fault_attempted=False
        chain.sql=mock.Mock(side_effect=pm.Blocked('commit outcome unknown'))
        with self.assertRaisesRegex(pm.Blocked,'commit outcome unknown'):
            pm.Chain.install_fault(chain,'postmortem.created','reader')
        self.assertTrue(chain.fault_attempted)
        chain.owner.assert_called_once();chain.frozen.assert_called_once()

    def test_readback_root_text_mismatch_never_claims_pass(self):
        chain,_,review,_=self.bare();chain.postmortem='e'*32
        chain.call=mock.Mock(side_effect=[({},{}),({'items':[dict(id=chain.postmortem,incident_id=chain.case,
            workflow_status='review',root_cause='wrong',actions='unknown / 1',duration=None)]},{})])
        with self.assertRaisesRegex(AssertionError,'PM second-API body/status'):pm.Chain.readback(chain,'review',review)
        chain.marker_case.assert_not_called()

    def test_restore_failure_still_runs_original_cleanup_and_never_passes(self):
        chain,_,_,_=self.bare();chain.remove_fault.side_effect=pm.Blocked('deadline exhausted')
        with (mock.patch.object(pm.ro.Chain,'cleanup') as cleanup,mock.patch.object(pm,'emit') as emit,
              mock.patch.object(pm.socket,'socket') as sock):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaisesRegex(pm.Blocked,'deadline exhausted'):chain.cleanup()
        cleanup.assert_called_once();emit.assert_not_called()

    def test_record_readback_keeps_unknown_duration_and_exact_close_revision(self):
        i=self.identity();row=dict(id='e'*32,incident_id=i['case'],source_revision=i['revision'],status='review',
            title='UUID title',severity='P2',action_count=1,duration=None,unknown_fields=['duration'],
            resolved='2026-10-05T00:00:00Z',created_at='2026-10-05T00:00:00Z')
        pm.verify_record({'items':[row]},'e'*32,i['case'],i['revision'],'review','UUID title')
        for key,value in [('source_revision','case:8'),('status','published'),('duration','1m'),('unknown_fields',[]),('action_count',0)]:
            with self.assertRaises(AssertionError):pm.verify_record({'items':[dict(row,**{key:value})]},'e'*32,i['case'],i['revision'],'review','UUID title')

if __name__=='__main__':unittest.main()

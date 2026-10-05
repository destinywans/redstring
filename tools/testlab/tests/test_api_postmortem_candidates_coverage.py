import contextlib
import io
import sys
import unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_postmortem_candidates_coverage as candidate

class CandidateTests(unittest.TestCase):
    def good(self):
        return {'items':[dict(id='INC-owned',title='UUID title',severity='P2',
                source_revision='case:9',resolved='2026-10-05T00:00:00Z',duration=None,
                evidence=None,unknown_fields=['duration','evidence'])]}

    def test_source_revision_rejects_empty_wrong_owner_and_synthetic_duration(self):
        candidate.assert_candidate(self.good(),'INC-owned','UUID title',9)
        for field,value in [('id','INC-other'),('title','other'),('severity','P1'),
                            ('source_revision','ops:9'),('source_revision',''),
                            ('duration','0m'),('evidence',0),('unknown_fields',[]),('resolved',None)]:
            row=dict(self.good()['items'][0],**{field:value})
            with self.assertRaises(AssertionError):
                candidate.assert_candidate({'items':[row]},'INC-owned','UUID title',9)
        for value in ({'items':[]},{'items':self.good()['items']*2}):
            with self.assertRaises(AssertionError):candidate.assert_candidate(value,'INC-owned','UUID title',9)

    def test_inventory_excludes_report_creation_and_full_business_chain(self):
        contract=candidate.Contract(candidate.ROOT)
        self.assertEqual(contract.canonical('GET','/api/v1/postmortems/candidates')[0],'/api/v1/postmortems/candidates')
        self.assertEqual(contract.canonical('POST','/api/v1/incidents/INC-owned/status')[0],'/api/v1/incidents/{id}/status')
        for method,path in [('POST','/postmortems'),('POST','/postmortems/PM-owned/publish'),
                            ('GET','/postmortems/draft'),('POST','/runbooks'),('POST','/uploads'),
                            ('POST','/canvases')]:
            with self.assertRaises(candidate.Blocked):contract.canonical(method,'/api/v1'+path)
        with self.assertRaises(candidate.Blocked):contract.canonical('GET','https://remote.invalid/api/v1/postmortems/candidates')

    def test_private_globals_do_not_change_shared_helper(self):
        self.assertIsNot(candidate.ro,shared)
        self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(candidate.ro.PORTS,(18628,18629))
        self.assertEqual(candidate.TARGETS,(('GET','/postmortems/candidates'),))

    def test_identity_guard_checks_each_independent_input(self):
        data=dict(source_tree_sha=candidate.SOURCE_SHA,binary_sha256=candidate.BINARY_SHA)
        candidate.content.frozen_identity(data,candidate.PG_CID)
        for changed,cid in [(dict(data,source_tree_sha='0'*64),candidate.PG_CID),
                            (dict(data,binary_sha256='0'*64),candidate.PG_CID),(data,'0'*64)]:
            with self.assertRaises(AssertionError):candidate.content.frozen_identity(changed,cid)

    def test_live_opt_in_stops_before_doctor_or_lock(self):
        with (mock.patch.object(sys,'argv',['candidates']),mock.patch.object(candidate,'doctor') as doctor,
              mock.patch.object(candidate.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):candidate.main()
        doctor.assert_not_called();opened.assert_not_called()

    def test_storage_fault_only_targets_invocation_role_select(self):
        chain=candidate.Chain.__new__(candidate.Chain);chain.db_user='readonly_owned';chain.sql=mock.Mock()
        chain.storage_select(False);chain.storage_select(True)
        sql=[c.args[0] for c in chain.sql.call_args_list]
        self.assertEqual(sql,['REVOKE SELECT ON v3_incident_case_status_events FROM "readonly_owned";',
                             'GRANT SELECT ON v3_incident_case_status_events TO "readonly_owned";'])
        self.assertTrue(all('INSERT' not in s and 'UPDATE' not in s and 'DELETE' not in s for s in sql))

    def fake_chain(self, failure=False, corrupt=False):
        chain=candidate.Chain.__new__(candidate.Chain)
        chain.incident='INC-owned';chain.marker='UUID title';chain.nonce='a'*32
        chain.roles={'reader':'owned-reader'};chain.db_user='owned-role'
        ledger=dict(postmortems=0,cases=[{'id':chain.incident}],events=[{'id':9}],audit=[])
        chain.ledger=mock.Mock(return_value=ledger);chain.sql=mock.Mock();chain.marker_case=mock.Mock()
        chain.storage_select=mock.Mock();calls=[]
        value=self.good()
        def call(user,method,path,body=None,**kwargs):
            calls.append((user,method,kwargs))
            if method=='POST':
                self.assertEqual(body['expected_revision'],1);self.assertEqual(body['status'],'resolved')
                return dict(event={'id':9},current_status='resolved',current_revision=2,current_resolved_status_event_id=9),{}
            if kwargs.get('want')==503:
                if failure:raise AssertionError('original failure retained')
                return {'error':{'code':'postmortems_unavailable'}},{}
            if kwargs.get('want') in (401,403):return {'error':{'code':kwargs['code']}},{}
            if len(calls)==1 or user in ('denied','hidden') or (user=='reader' and chain.sql.called):return {'items':[]},{}
            if kwargs.get('validate'):kwargs['validate'](value)
            if corrupt and kwargs.get('scenario')=='idempotency':return {'items':[]},{}
            return value,{}
        chain.call=call
        return chain,calls

    def test_storage_failure_restores_grant_and_does_not_emit_pass(self):
        chain,_=self.fake_chain(failure=True)
        with self.assertRaisesRegex(AssertionError,'original failure retained'):chain.candidates()
        self.assertEqual([c.args[0] for c in chain.storage_select.call_args_list],[False,True])
        self.assertNotIn('candidates.storage_failure',[c.args[0] for c in chain.marker_case.call_args_list])
        self.assertNotIn('candidates.recovery',[c.args[0] for c in chain.marker_case.call_args_list])

    def test_duplicate_read_drift_fails_before_fault_injection(self):
        chain,_=self.fake_chain(corrupt=True)
        with self.assertRaisesRegex(AssertionError,'GET duplicate changed'):chain.candidates()
        chain.storage_select.assert_not_called()

    def test_success_exercises_cas_permission_fault_and_exact_markers(self):
        chain,calls=self.fake_chain();chain.candidates()
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],list(candidate.REQUIRED_CASES)[1:])
        self.assertEqual([c.args[0] for c in chain.storage_select.call_args_list],[False,True])
        self.assertTrue(any(user is None and kw.get('want')==401 for user,method,kw in calls))
        self.assertTrue(any(user=='hidden' and kw.get('want')==403 for user,method,kw in calls))
        self.assertEqual(chain.ledger.call_count,3)
        self.assertIn("module='knowledge'",chain.sql.call_args.args[0])

    def test_cleanup_failure_cannot_announce_port_or_database_success(self):
        chain=candidate.Chain.__new__(candidate.Chain)
        with (mock.patch.object(candidate.ro.Chain,'cleanup',side_effect=candidate.Blocked('unknown owned outcome')),
              mock.patch.object(candidate,'emit') as emit,mock.patch.object(candidate.socket,'socket') as sock):
            with self.assertRaises(candidate.Blocked):chain.cleanup()
        emit.assert_not_called();sock.assert_not_called()

if __name__=='__main__':unittest.main()

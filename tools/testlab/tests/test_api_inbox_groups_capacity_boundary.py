import contextlib, copy, datetime, hashlib, io, json, sys, unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_inbox_groups_capacity_boundary as cap
import api_readonly_chain as shared

class CapacityBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.anchor=datetime.datetime(2026,10,6,0,0,tzinfo=datetime.timezone.utc)
    def test_isolated_ports_exact_two_queries(self):
        self.assertIsNot(cap.ro,shared);self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(cap.ro.PORTS,(18900,18901));c=cap.Contract(cap.ROOT)
        for path in ('/api/v1/inbox?filter=mine','/api/v1/alert-groups?state=resolved'):
            self.assertEqual(c.canonical('GET',path)[1],'main')
        for path in ('/api/v1/inbox','/api/v1/inbox?filter=all','/api/v1/inbox?filter=mine&filter=mine',
                     '/api/v1/alert-groups?state=firing','https://evil.invalid/api/v1/inbox?filter=mine'):
            with self.assertRaises((AssertionError,cap.Blocked)):c.canonical('GET',path)
        with self.assertRaises((AssertionError,cap.Blocked)):c.canonical('POST','/api/v1/incidents')
    def test_26_batched_envelopes_exact_5001_unique_past_resolved_occurrences(self):
        batches=[cap.fixture_batch('source','nonce','title',self.anchor,i*200,200) for i in range(25)]
        batches.append(cap.fixture_batch('source','nonce','title',self.anchor,5000,1))
        self.assertEqual([len(b['alerts']) for b in batches],[200]*25+[1])
        alerts=[a for b in batches for a in b['alerts']]
        self.assertEqual(len({a['startsAt'] for a in alerts}),5001)
        self.assertEqual({a['fingerprint'] for a in alerts},{'source'})
        for a in alerts:
            self.assertEqual(a['status'],'resolved')
            self.assertEqual(datetime.datetime.fromisoformat(a['endsAt'])-datetime.datetime.fromisoformat(a['startsAt']),datetime.timedelta(seconds=1))
            self.assertEqual(a['labels'],dict(incident_scope='nonce',alertname='title',severity='critical'))
        self.assertTrue(all(len(json.dumps(b).encode())<1<<20 for b in batches))
    def test_batch_invalid_bounds_stop(self):
        for start,count in ((-1,200),(0,0),(0,201),(5001,1),(5000,2),(True,1),(0,True)):
            with self.assertRaises(AssertionError):cap.fixture_batch('s','n','t',self.anchor,start,count)
        with self.assertRaises(AssertionError):cap.fixture_batch('s','n','t',self.anchor.replace(tzinfo=None),0,1)
    def inbox(self):
        return dict(items=[],counts=dict(all=5000,mine=0,mentions=None,subscribed=None),partial=True,
            window='today_and_yesterday',all_scope='persisted_alert_incident_updates',mine_scope='assigned_alert_incidents',
            unavailable=['unread_state','mentions','subscriptions','non_alert_event_types'],timezone='UTC',as_of=self.anchor.isoformat())
    def test_inbox_count_and_honest_partial_are_required(self):
        value=self.inbox();headers={'Cache-Control':'no-store'}
        cap.verify_inbox(value,headers,5000,self.anchor,self.anchor)
        for key,replacement in (('counts',dict(all=4999,mine=0,mentions=None,subscribed=None)),('items',[{}]),
                                ('partial',False),('as_of',(self.anchor-datetime.timedelta(seconds=1)).isoformat()),('unavailable',[])):
            with self.assertRaises(AssertionError):cap.verify_inbox(dict(value,**{key:replacement}),headers,5000,self.anchor,self.anchor)
        with self.assertRaises(AssertionError):cap.verify_inbox(value,{},5000,self.anchor,self.anchor)
    def group(self):
        source='own-source';app='own-app';env='own-env'
        return dict(id='group:'+hashlib.sha256(('\0'.join((env,app,source,source))).encode()).hexdigest()[:24],
            severity='P1',labels=[dict(k='application_id',v=app),dict(k='environment',v='test'),dict(k='source_id',v=source),dict(k='fingerprint',v=source)],
            service=app,source_id=source,fingerprint=source,environment_id=env,environment_key='test',count=5000,
            count_kind='persisted_incidents',state='resolved',since=self.anchor.isoformat(),notify=dict(channel=None,text='平台无通知记录；外部通知状态未知',status=None,scope='platform_queue'),
            badges=[],owner=None,inhibited=None,instances=[],local_grouping=['environment_id','application_id','source_id','fingerprint'],partial=True,
            unavailable=['provider_grouping','provider_labels','current_value','metric_series','pending_progress','silence_state','inhibition','external_notification_status','single_owner'])
    def groups(self):
        return dict(items=[self.group()],counts=dict(all=1,firing=0,pending=0,resolved=1,silenced=None),
            aggregation='local_persisted_incidents',partial=True,unavailable=['provider_grouping','silence_state','inhibition','external_notification_status'])
    def test_groups_exact_identity_count_and_complete_dto(self):
        value=self.groups();cap.verify_groups(value,'own-app','own-env','own-source',self.anchor,5000)
        for field,replacement in (('count',4999),('source_id','other'),('environment_id','other'),('instances',[{}]),('notify',{}),('partial',False),('owner','invented'),('since',(self.anchor+datetime.timedelta(seconds=1)).isoformat())):
            bad=copy.deepcopy(value);bad['items'][0][field]=replacement
            with self.assertRaises(AssertionError):cap.verify_groups(bad,'own-app','own-env','own-source',self.anchor,5000)
        bad=copy.deepcopy(value);del bad['items'][0]['inhibited']
        with self.assertRaises(AssertionError):cap.verify_groups(bad,'own-app','own-env','own-source',self.anchor,5000)
    def test_5001_refusal_exact_and_not_empty_fake_success(self):
        v=dict(error=dict(code='pagination_required',message='Incident results exceed the current V3 list contract'))
        cap.verify_limit(v,{'Content-Type':'application/json; charset=utf-8'})
        for invalid in ({},dict(items=[]),dict(error=dict(code='events_unavailable',message='x'))):
            with self.assertRaises(AssertionError):cap.verify_limit(invalid,{'Content-Type':'application/json'})
        with self.assertRaises(AssertionError):cap.verify_limit(v,{'Content-Type':'text/html'})
    def test_no_registration_even_fixture_or_success(self):
        row=cap.evidence(dict(registration_evidence=True,scenario='normal',status='passed'))
        self.assertFalse(row['registration_evidence']);self.assertEqual(row['test_name'],'FormalInboxGroupsCapacityBoundary')
    def test_no_live_stops_before_doctor_or_lease(self):
        with mock.patch.object(cap,'doctor') as doctor,mock.patch.object(cap.ro,'exclusive') as lease,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):cap.main([])
        doctor.assert_not_called();lease.assert_not_called()
    def test_ingress_failure_not_retried(self):
        chain=cap.Chain.__new__(cap.Chain);chain.source_id='s';chain.nonce='n';chain.marker='t'
        chain.call=mock.Mock(side_effect=AssertionError('first failed ingress'))
        with self.assertRaises(AssertionError):chain.ingress(self.anchor,0,200,0)
        self.assertEqual(chain.call.call_count,1)
    def test_read_write_drift_prevents_ledger_completion(self):
        chain=cap.Chain.__new__(cap.Chain);chain.app='own-app';chain.env='own-env';chain.source_id='own-source';chain.anchor=self.anchor
        chain.frozen=mock.Mock();chain.marker_case=mock.Mock();chain.work=Path('/private/tmp/not-created-by-test')
        chain.ledger=mock.Mock(side_effect=[{'table':'before'},{'table':'after'}]);chain.call=mock.Mock(side_effect=[(dict(error=dict(code='pagination_required',message='Incident results exceed the current V3 list contract')),{'Content-Type':'application/json'})]*2)
        with mock.patch.object(cap.ro,'_private'):
            with self.assertRaisesRegex(AssertionError,'must not write'):chain.read_boundary(5001)
        self.assertEqual(chain.call.call_count,2)
    def test_frozen_rejects_before_actual_call(self):
        chain=cap.Chain.__new__(cap.Chain);chain.frozen=mock.Mock(side_effect=cap.Blocked('drift'))
        with mock.patch.object(cap.ro.Chain,'call') as request:
            with self.assertRaises(cap.Blocked):chain.call('reader','GET','/api/v1/inbox?filter=mine')
        request.assert_not_called()

if __name__=='__main__':unittest.main()

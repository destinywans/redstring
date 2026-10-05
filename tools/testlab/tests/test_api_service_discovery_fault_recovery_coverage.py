import contextlib,copy,datetime,io,json,sys,unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_service_discovery_real_source_coverage as passed
import api_service_discovery_fault_recovery_coverage as fault

UID='3cde8a75-5dd0-4ce7-b8a5-aa62449721ed';NEW_UID='c18f8c0b-2918-4dcc-9137-cb315408a20b'
RUN='6075eec3-7b7b-4a1e-b330-45778fb1a0ac'

class FaultRecoveryTests(unittest.TestCase):
    def chain(self):
        chain=fault.Chain.__new__(fault.Chain);chain.run_id=RUN;chain.source_id='source-real'
        chain.work=fault.ROOT/'.local/testlab/nonexecuted-source-fault-tests'
        chain.auth=[dict(path=collection+'/'+spec['metadata']['name'],uid=UID,spec=spec) for collection,spec in fault.base.auth_specs(RUN)]
        chain.unknown=[];chain.binding_absent=None;chain.binding_history=[];chain.expected={UID:{}}
        chain.token_expiry=datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=600)
        return chain

    def ledger(self,error=fault.ERROR,attempt=10,success=0):
        now=datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(seconds=30)
        stamp=lambda n:(now+datetime.timedelta(seconds=n)).isoformat()
        row=dict(id='real-resource',source_id='source-real',source_kind='kubernetes',is_mock=False,kind='deployment',external_id=UID,
            observed_at=stamp(success),attributes=dict(environment_key='test',inventory_presence='present',resource_type='Deployment',uid=UID))
        return dict(source=dict(id='source-real',kind='kubernetes',environment_key='test',resource_count=1,
            last_error=error,last_attempt_at=stamp(attempt),last_success_at=stamp(success),updated_at=stamp(attempt)),
            resources=[row],relations=[dict(id='edge',updated_at='fixed')],links=[])

    def test_private_reuse_leaves_passed_module_and_ports_unchanged(self):
        self.assertIsNot(fault.base,passed);self.assertIsNot(fault.ro,passed.ro)
        self.assertEqual(passed.PORTS,(18774,18775));self.assertEqual(fault.PORTS,(18776,18777))
        self.assertEqual(fault.base.MODULE,fault.MODULE);self.assertEqual(passed.MODULE,'formal-api-service-discovery-real')
        self.assertIn('tests/test_api_service_discovery_fault_recovery_coverage.py',fault.hashes())
        self.assertIn('api_service_discovery_real_source_coverage.py',fault.hashes())

    def test_error_attempt_only_advances_attempt_not_complete_ledger_or_success(self):
        anchor=self.ledger();current=copy.deepcopy(anchor)
        current['source']['last_attempt_at']=(fault.stamp(anchor['source']['last_attempt_at'])+datetime.timedelta(seconds=10)).isoformat()
        current['source']['updated_at']=current['source']['last_attempt_at']
        fault.assert_failure_frozen(anchor,current)
        for section,key,new in [('source','last_success_at',current['source']['last_attempt_at']),('source','environment_key','staging'),
                                ('source','resource_count',0),('source','last_error',''),('source','last_attempt_at',anchor['source']['last_attempt_at'])]:
            bad=copy.deepcopy(current);bad[section][key]=new
            with self.assertRaises(AssertionError):fault.assert_failure_frozen(anchor,bad)
        for section in ('resources','relations','links'):
            bad=copy.deepcopy(current);bad[section].append(dict(fake='SQL snapshot'))
            with self.assertRaises(AssertionError):fault.assert_failure_frozen(anchor,bad)

    def test_inflight_success_before_first403_is_legal_and_becomes_anchor(self):
        chain=self.chain();baseline=self.ledger(error='',attempt=0,success=0)
        anchor=copy.deepcopy(baseline);anchor['source']['last_error']=fault.ERROR
        for key in ('last_attempt_at','updated_at'):anchor['source'][key]=(fault.stamp(baseline['source']['last_attempt_at'])+datetime.timedelta(seconds=10)).isoformat()
        anchor['source']['last_success_at']=(fault.stamp(baseline['source']['last_success_at'])+datetime.timedelta(seconds=1)).isoformat()
        anchor['resources'][0]['observed_at']=anchor['source']['last_success_at']
        with mock.patch.object(chain,'source_ledger',return_value=anchor):
            self.assertIs(chain.wait_error(baseline,999),anchor)
        self.assertNotEqual(baseline['source']['last_success_at'],anchor['source']['last_success_at'])

    def test_complete_snapshot_preserves_historical_missing_rows(self):
        chain=self.chain();value=self.ledger()
        old=copy.deepcopy(value['resources'][0]);old['id']='old';old['external_id']='old-uid'
        old['attributes']['inventory_presence']='missing';value['resources'].append(old)
        chain.complete(value)
        value['source']['resource_count']=2
        with self.assertRaises(AssertionError):chain.complete(value)

    def test_second_failure_must_be_natural_new_attempt_and_exact403(self):
        chain=self.chain();anchor=self.ledger();current=copy.deepcopy(anchor)
        current['source']['last_attempt_at']=(fault.stamp(anchor['source']['last_attempt_at'])+datetime.timedelta(seconds=10)).isoformat()
        with mock.patch.object(chain,'source_ledger',return_value=current):self.assertIs(chain.wait_second_failure(anchor,999),current)
        current['source']['last_error']='Kubernetes inventory HTTP 401 for /api/v1/namespaces'
        with mock.patch.object(chain,'source_ledger',return_value=current):
            with self.assertRaises(AssertionError):chain.wait_second_failure(anchor,999)

    def test_poll_deadline_is_shared_no_sql_source_or_clock_mutation(self):
        chain=self.chain();chain.database='own';chain.db_user='own';deadline=777
        with mock.patch.object(chain,'frozen'),mock.patch.object(chain,'pg',return_value=json.dumps(self.ledger())) as pg:
            chain.source_ledger(deadline)
        statement=pg.call_args.args[0]
        self.assertTrue(statement.startswith('BEGIN READ ONLY; SELECT'))
        self.assertEqual(pg.call_args.kwargs['deadline'],deadline)
        for forbidden in ('UPDATE ','INSERT ','DELETE ','set_config','clock_timestamp'):
            self.assertNotIn(forbidden,statement)
        for required in ('cmdb_resources','cmdb_resource_relations','cmdb_resource_applications'):
            self.assertIn(required,statement)

    def test_revoke_exact_uid_first_then_known_absence_not_cleanup_active(self):
        chain=self.chain();record=copy.deepcopy(chain.auth[-1]);calls=[]
        value=copy.deepcopy(record['spec']);value['metadata']['uid']=UID
        with mock.patch.object(chain,'kube',side_effect=lambda *a,**kw:calls.append((a,kw)) or value),\
             mock.patch.object(chain,'journal') as journal,mock.patch.object(chain,'marker_case'),mock.patch.object(fault.time,'monotonic',return_value=100):
            chain.revoke_binding()
        self.assertEqual([x[0][0] for x in calls],['GET','DELETE','GET'])
        self.assertEqual({x[1]['deadline'] for x in calls},{104});self.assertEqual(calls[1][0][2]['preconditions'],dict(uid=UID))
        self.assertEqual(calls[2][1]['want'],404);self.assertEqual(chain.binding_absent,record)
        self.assertEqual(len(chain.auth),2);self.assertEqual(journal.call_count,2)

    def test_revoke_uid_mismatch_sends_no_delete(self):
        chain=self.chain();value=copy.deepcopy(chain.auth[-1]['spec']);value['metadata']['uid']=NEW_UID
        with mock.patch.object(chain,'kube',return_value=value) as kube:
            with self.assertRaises(AssertionError):chain.revoke_binding()
        self.assertEqual(kube.call_count,1);self.assertEqual(len(chain.auth),3)

    def absent(self):
        chain=self.chain();chain.binding_absent=chain.auth.pop();return chain

    def test_restore_pristine_same_spec_and_new_uid_active_cleanup_record(self):
        chain=self.absent();spec=copy.deepcopy(chain.binding_absent['spec']);value=copy.deepcopy(spec);value['metadata']['uid']=NEW_UID
        def kube(method,path,body=None,**kw):
            if method=='POST':
                self.assertEqual(body,spec);self.assertNotIn('uid',body['metadata']);self.assertTrue(chain.unknown)
                self.assertEqual(chain.binding_history[-1]['phase'],'create_intent')
                self.assertEqual(kw['want'],201)
            return value
        with mock.patch.object(chain,'kube',side_effect=kube),mock.patch.object(chain,'journal'),\
             mock.patch.object(chain,'marker_case'),mock.patch.object(fault.ro,'_private'):
            chain.recreate_binding()
        self.assertEqual(chain.auth[-1]['uid'],NEW_UID);self.assertEqual(chain.unknown,[])
        self.assertEqual(chain.binding_absent['uid'],UID);self.assertEqual(len(chain.auth),3)
        self.assertEqual([r['uid'] for r in chain.auth if r['spec']['kind']=='ClusterRoleBinding'],[NEW_UID])

    def test_unknown_recreate_retains_intent_no_adoption_or_name_delete(self):
        chain=self.absent();first=RuntimeError('unknown create outcome')
        def kube(method,*args,**kwargs):
            if method=='POST':raise first
            return {}
        with mock.patch.object(chain,'kube',side_effect=kube) as kube_mock,mock.patch.object(chain,'journal'):
            with self.assertRaises(RuntimeError) as caught:chain.recreate_binding()
        self.assertIs(caught.exception,first);self.assertEqual(len(chain.auth),2)
        self.assertEqual(chain.unknown,[chain.binding_absent['path']]);self.assertEqual([c.args[0] for c in kube_mock.call_args_list],['GET','POST'])

    def test_post201_evidence_write_failure_keeps_known_new_uid_for_exact_cleanup(self):
        chain=self.absent();value=copy.deepcopy(chain.binding_absent['spec']);value['metadata']['uid']=NEW_UID
        error=OSError('private response write failed after authenticated201')
        with mock.patch.object(chain,'kube',return_value=value) as requests,mock.patch.object(chain,'journal'),\
             mock.patch.object(fault.ro,'_private',side_effect=error),mock.patch.object(chain,'marker_case') as marker:
            with self.assertRaises(OSError) as caught:chain.recreate_binding()
        self.assertIs(caught.exception,error);self.assertEqual(requests.call_count,2);marker.assert_not_called()
        self.assertEqual(chain.unknown,[]);self.assertEqual(len(chain.auth),3)
        self.assertEqual(chain.auth[-1]['uid'],NEW_UID);self.assertEqual(chain.binding_absent['uid'],UID)
        calls=[]
        def kube(method,path,body=None,**kw):
            record=next(r for r in chain.auth if r['path']==path);response=copy.deepcopy(record['spec'])
            response['metadata']['uid']=record['uid'];calls.append((method,path,body,kw));return response
        with mock.patch.object(chain,'kube',side_effect=kube),mock.patch.object(fault.ro,'_private'):
            chain.cleanup_auth()
        self.assertEqual(len(calls),9);self.assertEqual(calls[1][0],'DELETE')
        self.assertEqual(calls[1][2]['preconditions'],dict(uid=NEW_UID))
        self.assertFalse(any(method=='POST' for method,_,_,_ in calls))

    def test_same_uid_recreate_does_not_adopt_response(self):
        chain=self.absent();value=copy.deepcopy(chain.binding_absent['spec']);value['metadata']['uid']=UID
        with mock.patch.object(chain,'kube',return_value=value),mock.patch.object(chain,'journal'),mock.patch.object(fault.ro,'_private'):
            with self.assertRaises(AssertionError):chain.recreate_binding()
        self.assertEqual(len(chain.auth),2);self.assertTrue(chain.unknown)

    def test_cleanup_after_restoration_uses_only_new_uid_shared_four_seconds(self):
        chain=self.absent();chain.auth.append(dict(chain.binding_absent,uid=NEW_UID));calls=[]
        def kube(method,path,body=None,**kw):
            record=next(r for r in chain.auth if r['path']==path);value=copy.deepcopy(record['spec']);value['metadata']['uid']=record['uid']
            calls.append((method,path,body,kw));return value
        with mock.patch.object(chain,'kube',side_effect=kube),mock.patch.object(fault.time,'monotonic',return_value=100),mock.patch.object(fault.ro,'_private'):
            chain.cleanup_auth()
        self.assertEqual(len(calls),9);self.assertEqual({x[3]['deadline'] for x in calls},{104})
        self.assertEqual(calls[1][2]['preconditions']['uid'],NEW_UID)

    def test_cleanup_before_recovery_never_recreates_binding_and_unknown_stays_blocked(self):
        chain=self.absent();chain.unknown=[chain.binding_absent['path']]
        def kube(method,path,body=None,**kw):
            self.assertNotIn('clusterrolebindings',path);self.assertNotEqual(method,'POST')
            record=next(r for r in chain.auth if r['path']==path);value=copy.deepcopy(record['spec']);value['metadata']['uid']=UID;return value
        with mock.patch.object(chain,'kube',side_effect=kube) as requests,mock.patch.object(fault.ro,'_private'):
            with self.assertRaises(AssertionError):chain.cleanup_auth()
        self.assertEqual(requests.call_count,6)

    def test_cleanup_inherits_original40_then_auth4_preserving_first_failure(self):
        chain=self.chain();first=RuntimeError('PG cleanup');events=[]
        def original(_):events.append('40');raise first
        def auth():events.append('4');raise ValueError('auth cleanup')
        with mock.patch.object(fault.ro.Chain,'cleanup',original),mock.patch.object(chain,'cleanup_auth',side_effect=auth):
            with self.assertRaises(RuntimeError) as caught:chain.cleanup()
        self.assertIs(caught.exception,first);self.assertEqual(events,['40','4'])

    def test_token_remaining_guard_no_renewal_or_ttl_change(self):
        chain=self.chain();chain.token_budget(130)
        chain.token_expiry=datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=129)
        with self.assertRaises(AssertionError):chain.token_budget(130)
        chain.token_expiry=None
        with self.assertRaises(AssertionError):chain.token_budget(75)

    def test_formal_http_even_baseline_classified_failure_not_new_normal(self):
        chain=self.chain()
        with mock.patch.object(fault.base.Chain,'call',return_value=({},{})) as call:
            chain.call('reader','GET','/api/v1/services/discover',scenario='normal')
        self.assertEqual(call.call_args.kwargs['scenario'],'failure')

    def test_recovery_requires_new_success_clear_error_and_real_complete_source(self):
        chain=self.chain();anchor=self.ledger();current=copy.deepcopy(anchor)
        current['source']['last_error']='';current['source']['last_success_at']=(fault.stamp(anchor['source']['last_success_at'])+datetime.timedelta(seconds=20)).isoformat()
        current['resources'][0]['observed_at']=current['source']['last_success_at']
        with mock.patch.object(chain,'source_ledger',return_value=current):self.assertIs(chain.wait_recovery(anchor,999),current)
        current['resources'][0]['is_mock']=True
        with mock.patch.object(chain,'source_ledger',return_value=current):
            with self.assertRaises(AssertionError):chain.wait_recovery(anchor,999)

    def dto_fixture(self,chain,anchor,advance=0):
        original=dict(kind='Deployment',metadata=dict(uid=UID,name='real-workload',namespace='kube-system',labels={}),
            spec=dict(replicas=1,template=dict(spec=dict(containers=[dict(image='real:tag')]))))
        chain.expected={UID:original}
        observed=(fault.stamp(anchor['source']['last_success_at'])+datetime.timedelta(seconds=advance)).isoformat()
        return dict(cluster=fault.base.CLUSTER,registered=0,ambiguous_workloads=0,items=[dict(id='real-workload',
            namespace='kube-system',uid=UID,resource_id=fault.base.resource_id(chain.source_id,UID),kind='Deployment',
            environment_id='test',cluster=fault.base.CLUSTER,image='real:tag',replicas=1,owner_hint='Unknown',tier_hint='',
            matched=[],missing=[],source=dict(kind='kubernetes',id=chain.source_id,observed_at=observed,last_success_at=observed),
            unavailable_fields=['health','verified_tier','connector_coverage','runbook_gaps','verified_owner'])])

    def test_fault_pair_requires200_and_exact_old_observation_not_fresh_fabrication(self):
        chain=self.chain();anchor=self.ledger();value=self.dto_fixture(chain,anchor)
        def call(*args,**kw):kw['validate'](copy.deepcopy(value));return copy.deepcopy(value),{}
        with mock.patch.object(chain,'call',side_effect=call) as calls,mock.patch.object(fault.ro,'_private'):
            chain.pair(anchor,'cached')
            self.assertEqual(calls.call_count,2);self.assertEqual(calls.call_args.kwargs['index'],1)
        value=self.dto_fixture(chain,anchor,advance=1)
        with mock.patch.object(chain,'call',side_effect=call),mock.patch.object(fault.ro,'_private'):
            with self.assertRaises(AssertionError):chain.pair(anchor,'cached')

    def test_recovery_pair_allows_actual_natural_refresh_but_not_mixed_source_times(self):
        chain=self.chain();anchor=self.ledger();values=[self.dto_fixture(chain,anchor,advance=n) for n in (1,2)]
        def call(*args,**kw):
            value=values.pop(0);kw['validate'](value);return value,{}
        with mock.patch.object(chain,'call',side_effect=call),mock.patch.object(fault.ro,'_private'):
            chain.pair(anchor,'recovered')
        bad=self.dto_fixture(chain,anchor,advance=1)
        bad['items'][0]['source']['observed_at']=anchor['source']['last_success_at']
        values=[bad]
        with mock.patch.object(chain,'call',side_effect=call),mock.patch.object(fault.ro,'_private'):
            with self.assertRaises(AssertionError):chain.pair(anchor,'recovered')

    def test_expired_poll_window_cannot_spin_or_retry_to_green(self):
        chain=self.chain();anchor=self.ledger()
        with mock.patch.object(chain,'source_ledger',return_value=anchor) as read,\
             mock.patch.object(fault.time,'monotonic',return_value=100),mock.patch.object(fault.time,'sleep') as sleep:
            with self.assertRaises(fault.Blocked):chain.wait_second_failure(anchor,99)
        self.assertEqual(read.call_count,1);sleep.assert_not_called()

    def test_explicit_live_required_before_admission_or_lease(self):
        with mock.patch.object(fault,'doctor') as doctor,mock.patch.object(fault.os,'open') as lease:
            with self.assertRaises(SystemExit),contextlib.redirect_stderr(io.StringIO()):fault.main([])
        doctor.assert_not_called();lease.assert_not_called()

if __name__=='__main__':unittest.main()

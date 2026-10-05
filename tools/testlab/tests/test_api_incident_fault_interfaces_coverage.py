import contextlib
import copy
import io
import json
import re
import sys
import unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_incident_fault_interfaces_coverage as incident

class IncidentFaultTests(unittest.TestCase):
    def test_private_module_and_single_post_inventory(self):
        self.assertIsNot(shared,incident.ro)
        self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(incident.ro.PORTS,(18740,18741))
        contract=incident.Contract(incident.ROOT)
        self.assertEqual(contract.canonical('POST','/api/v1/incidents')[0],'/api/v1/incidents')
        for verb,path in [('GET','/incidents'),('POST','/incidents/INC-owned/status'),
                          ('POST','/canvases'),('POST','/postmortems'),('POST','/runbooks')]:
            with self.assertRaises(incident.Blocked): contract.canonical(verb,'/api/v1'+path)
        for path in ('https://remote.invalid/api/v1/incidents','/api/v1/incidents?q=1','/api/v1/incidents#x'):
            with self.assertRaises((incident.Blocked,AssertionError)):contract.canonical('POST',path)

    def test_frozen_inputs_individually_guarded(self):
        data=dict(source_tree_sha=incident.SOURCE_SHA,binary_sha256=incident.BINARY_SHA,
                  runtime_sha=incident.RUNTIME_SHA)
        incident.frozen_identity(data,incident.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError):incident.frozen_identity(dict(data,**{key:'0'*64}),incident.PG_CID)
        with self.assertRaises(AssertionError):incident.frozen_identity(data,'0'*64)

    def test_no_live_stops_before_lock_or_doctor(self):
        with (mock.patch.object(sys,'argv',['incident']),mock.patch.object(incident.os,'open') as opened,
              mock.patch.object(incident,'doctor') as doctor,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):incident.main()
        opened.assert_not_called();doctor.assert_not_called()

    def test_fault_exact_native_action_actor_application_environment(self):
        name,create,drop=incident.fault_statements('a'*32,'b'*32,"app'quoted")
        self.assertIn('incident_audit_fail_'+'a'*32,name)
        self.assertIn("NEW.action='incident.create'",create)
        self.assertIn("NEW.actor_id='"+'b'*32+"'",create)
        self.assertIn("NEW.application_id='app''quoted'",create)
        self.assertIn("NEW.changes->>'environment_id'='test'",create)
        self.assertNotIn('v3.incident.created',create)
        self.assertTrue(create.startswith('BEGIN;'));self.assertTrue(create.endswith('COMMIT;'))
        self.assertIn('DROP TRIGGER IF EXISTS '+name,drop)
        self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()',drop)
        for bad in ('', 'x', 'a'*31, "a';DROP"):
            with self.assertRaises(AssertionError):incident.fault_statements(bad,'b'*32,'app')
            with self.assertRaises(AssertionError):incident.fault_statements('a'*32,bad,'app')

    def body(self):
        return dict(incident.payload('app-owned','alert-owned','故'*300),impact='影'*2000,commander='人'*200)

    def before(self):
        return dict(cases=[],links=[],status_events=[],timeline=[],investigations=0,events=0,
                    alerts=[dict(id='alert-owned',application_id='app-owned',status='active')],
                    audit=[dict(action='service.write',target='app-owned')])

    def after(self):
        b=self.before();body=self.body();result=dict(id='INC-'+'f'*32)
        b['cases']=[dict(id=result['id'],application_id='app-owned',environment_key='test',
            actor_id='b'*32,request_key='incident-'+'a'*32,title=body['title'],severity='P2',
            title_provenance='human_confirmed',severity_provenance='human_confirmed',
            impact_provenance='human_confirmed',commander_provenance='human_confirmed',request_hash='e'*64,
            impact=body['impact'],commander_text=body['commander'],status='active',status_revision=1,
            investigation_id=None,resolved_status_event_id=None,resolved_at=None)]
        b['links']=[dict(case_id=result['id'],alert_id='alert-owned')]
        b['audit'].append(dict(action='incident.create',actor_id='b'*32,actor_kind='user',
            application_id='app-owned',target=result['id'],outcome='succeeded',
            changes=dict(environment_id='test',alert_ids=['alert-owned'],investigation_id=None)))
        return b,result

    def assert_good(self,after,result):
        incident.assert_recovery(self.before(),after,result,self.body(),'b'*32,'app-owned','alert-owned','incident-'+'a'*32)

    def test_recovery_exact_stored_body_link_audit_and_no_chain(self):
        after,result=self.after();self.assert_good(after,result)
        for key,value in [('cases',[]),('links',[]),('status_events',[dict(id=1)]),
                          ('timeline',[dict(id=1)]),('investigations',1),('events',1),('alerts',[])]:
            with self.assertRaises(AssertionError):self.assert_good(dict(after,**{key:value}),result)
        for field,value in [('title','different'),('severity','P1'),('impact','different'),
                            ('actor_id','c'*32),('commander_text','different'),('status','resolved'),
                            ('status_revision',2),('investigation_id','INV-owned'),
                            ('request_key','wrong'),('resolved_at','2026-10-05'),('request_hash','wrong'),
                            ('title_provenance','ai'),('impact_provenance','unknown')]:
            bad=copy.deepcopy(after);bad['cases'][0][field]=value
            with self.assertRaises(AssertionError):self.assert_good(bad,result)
        for field,value in [('target','INC-other'),('actor_id','c'*32),('application_id','app-other'),
                            ('outcome','failed'),('changes',{}),('actor_kind','agent')]:
            bad=copy.deepcopy(after);bad['audit'][1][field]=value
            with self.assertRaises(AssertionError):self.assert_good(bad,result)
        bad=copy.deepcopy(after);bad['audit'][0]['target']='other'
        with self.assertRaises(AssertionError):self.assert_good(bad,result)
        with self.assertRaises(AssertionError):self.assert_good(after,dict(result,investigation_id='INV-owned'))

    def test_dto_boundaries_have_distinct_bounded_cases_and_keep_external_flags_off(self):
        body=self.body();cases=incident.invalid_payloads(body)
        self.assertEqual(len(cases),13)
        self.assertEqual(len({json.dumps(b,sort_keys=True) for b,_ in cases}),13)
        self.assertEqual([code for _,code in cases].count('invalid_body'),1)
        for invalid,_ in cases:
            self.assertFalse(any(invalid[k] for k in ('notify','war_room','status_page','open_canvas')))
            self.assertLess(len(json.dumps(invalid).encode()),16384)
        self.assertEqual(len(body['title']),300);self.assertEqual(len(body['impact']),2000)

    def bare(self):
        chain=incident.Chain.__new__(incident.Chain)
        chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.marker_case=mock.Mock()
        chain.nonce='a'*32;chain.users={'reader':'b'*32};chain.roles={'reader':'own-reader'}
        chain.app='app-owned';chain.alert='alert-owned';chain.marker='UUID title'
        chain.database='aiops_testlab_readonly_'+'a'*32;chain.db_user='readonly_'+'a'*32
        chain.owner_tag='owned:audit:'+chain.nonce
        return chain

    def test_rejected_case_write_drift_stops_before_marker(self):
        chain=self.bare();chain.call=mock.Mock();chain.ledger=mock.Mock(return_value=dict(self.before(),events=1))
        with self.assertRaisesRegex(AssertionError,'rejected incident request changed'):
            chain.reject(self.before(),self.body(),422,'invalid_incident')
        chain.marker_case.assert_not_called()

    def test_install_unknown_outcome_still_requests_fault_removal(self):
        chain=self.bare();chain.fault_attempted=False
        chain.sql=mock.Mock(side_effect=incident.Blocked('unknown commit'))
        with self.assertRaises(incident.Blocked):chain.install_fault()
        self.assertTrue(chain.fault_attempted)
        chain.owner.assert_called_once()

    def test_remove_readback_residual_preserves_pending_flag(self):
        chain=self.bare();chain.fault_attempted=True
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=True,function=False))])
        with self.assertRaisesRegex(AssertionError,'owned audit fault remains'):chain.remove_fault()
        self.assertTrue(chain.fault_attempted)
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        chain.remove_fault();self.assertFalse(chain.fault_attempted)

    def test_owner_read_only_uuid_tags_and_deadline_without_source_dependency(self):
        chain=self.bare()
        expected=dict(database=chain.database,role=chain.db_user,db_tag=chain.owner_tag,role_tag=chain.owner_tag)
        chain.pg=mock.Mock(return_value=json.dumps(expected))
        incident.Chain.owner(chain,deadline=1234)
        args,kwargs=chain.pg.call_args
        self.assertTrue(args[0].startswith('BEGIN READ ONLY; '));self.assertTrue(args[0].endswith('; COMMIT;'))
        self.assertEqual(args[1:],(chain.database,chain.db_user));self.assertEqual(kwargs,dict(deadline=1234))
        chain.frozen.assert_not_called()
        for key in expected:
            chain.pg.return_value=json.dumps(dict(expected,**{key:'foreign'}))
            with self.assertRaisesRegex(AssertionError,'invocation DB/role ownership changed'):
                incident.Chain.owner(chain,deadline=1234)

    def test_remove_steps_share_four_second_deadline_after_source_drift(self):
        chain=self.bare();chain.fault_attempted=True
        chain.frozen.side_effect=incident.Blocked('source changed')
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        with mock.patch.object(incident.time,'monotonic',return_value=100):chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual(chain.pg.call_count,2)
        for call in chain.pg.call_args_list:
            self.assertEqual(call.args[1:],(chain.database,chain.db_user))
            self.assertEqual(call.kwargs,dict(deadline=104))
        self.assertTrue(chain.pg.call_args_list[1].args[0].startswith('BEGIN READ ONLY; '))
        chain.frozen.assert_not_called();self.assertFalse(chain.fault_attempted)

    def test_expired_recovery_budget_blocks_before_any_native_call(self):
        chain=self.bare();chain.fault_attempted=True
        chain.owner=lambda deadline:incident.Chain.owner(chain,deadline=deadline)
        with (mock.patch.object(incident.time,'monotonic',side_effect=[100,105]),
              mock.patch.object(incident.ro,'inspect_pg') as inspect,
              mock.patch.object(incident.ro.subprocess,'run') as native):
            with self.assertRaisesRegex(incident.Blocked,'cleanup exhausted'):chain.remove_fault()
        inspect.assert_not_called();native.assert_not_called();self.assertTrue(chain.fault_attempted)

    def test_cleanup_budget_failure_still_runs_original_cleanup_without_pass(self):
        chain=self.bare();chain.fault_attempted=True
        chain.owner.side_effect=incident.Blocked('recovery deadline exhausted')
        chain.pg=mock.Mock()
        with (mock.patch.object(incident.ro.Chain,'cleanup') as cleanup,
              mock.patch.object(incident.socket,'socket') as sock,mock.patch.object(incident,'emit') as emit,
              mock.patch.object(incident.time,'monotonic',return_value=100)):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaisesRegex(incident.Blocked,'recovery deadline exhausted'):chain.cleanup()
        cleanup.assert_called_once();chain.pg.assert_not_called();emit.assert_not_called()
        self.assertTrue(chain.fault_attempted)

    def test_restore_pg_guard_still_requires_exact_container(self):
        with mock.patch.object(incident,'_inspect_pg',return_value=dict(Id='foreign')):
            with self.assertRaisesRegex(AssertionError,'exact owned PG container changed'):
                incident.inspect_pg('container')

    def test_http_fault_failure_removed_but_not_retried_or_announced_pass(self):
        chain=self.bare();chain.install_fault=mock.Mock();chain.remove_fault=mock.Mock()
        chain.reject=mock.Mock(side_effect=AssertionError('first red'));chain.call=mock.Mock()
        with self.assertRaisesRegex(AssertionError,'first red'):chain.audit_recovery(self.before(),self.body())
        chain.remove_fault.assert_called_once();chain.call.assert_not_called()
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],['empty_case_storage'])

    def test_install_failure_also_restores_without_recovery(self):
        chain=self.bare();chain.install_fault=mock.Mock(side_effect=incident.Blocked('first DDL'))
        chain.remove_fault=mock.Mock();chain.reject=mock.Mock();chain.call=mock.Mock()
        with self.assertRaises(incident.Blocked):chain.audit_recovery(self.before(),self.body())
        chain.remove_fault.assert_called_once();chain.reject.assert_not_called();chain.call.assert_not_called()

    def test_recovery_corruption_stops_before_repeat_or_revocation(self):
        chain=self.bare();chain.install_fault=mock.Mock();chain.remove_fault=mock.Mock();chain.reject=mock.Mock()
        after,result=self.after();after['cases'][0]['title']='corrupt'
        chain.call=mock.Mock(return_value=(result,{}));chain.ledger=mock.Mock(return_value=after);chain.sql=mock.Mock()
        with self.assertRaisesRegex(AssertionError,'recovered case body'):chain.audit_recovery(self.before(),self.body())
        self.assertEqual(chain.call.call_count,1);chain.sql.assert_not_called()
        self.assertNotIn('recovery',[c.args[0] for c in chain.marker_case.call_args_list])

    def test_success_has_exact_markers_and_current_grant_restoration(self):
        chain=self.bare()
        chain.install_fault=mock.Mock();chain.remove_fault=mock.Mock();chain.reject=mock.Mock()
        after,result=self.after();chain.ledger=mock.Mock(return_value=after)
        chain.call=mock.Mock(return_value=(result,{}));chain.sql=mock.Mock()
        chain.audit_recovery(self.before(),self.body())
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],
                         ['empty_case_storage','audit_rollback','fault_removed','recovery',
                          'recovery_repeat','current_authorization'])
        self.assertEqual(chain.call.call_count,3)
        self.assertEqual(chain.call.call_args_list[0].kwargs['index'],1)
        self.assertEqual(chain.sql.call_count,4)
        self.assertIn("action='change'",chain.sql.call_args_list[0].args[0])
        self.assertIn("action='view'",chain.sql.call_args_list[2].args[0])
        chain.remove_fault.assert_called_once()

    def test_current_permission_rejection_failure_still_restores_exact_grant(self):
        chain=self.bare();chain.install_fault=mock.Mock();chain.remove_fault=mock.Mock()
        after,result=self.after();chain.ledger=mock.Mock(return_value=after)
        chain.call=mock.Mock(return_value=(result,{}));chain.sql=mock.Mock()
        chain.reject=mock.Mock(side_effect=[None,None,None,AssertionError('revocation red')])
        with self.assertRaisesRegex(AssertionError,'revocation red'):
            chain.audit_recovery(self.before(),self.body())
        self.assertEqual(chain.sql.call_count,2)
        self.assertIn("'events','change','environment','test'",chain.sql.call_args_list[1].args[0])
        self.assertNotIn('current_authorization',[c.args[0] for c in chain.marker_case.call_args_list])

    def test_boundary_scope_and_external_flags_keep_exact_rejections(self):
        chain=self.bare();chain.reject=mock.Mock()
        chain.boundaries(self.before(),self.body())
        self.assertEqual(chain.reject.call_count,32)
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],
                         ['authorization','format_boundaries','scope_boundary','external_actions_rejected'])
        self.assertEqual([c.args[3] for c in chain.reject.call_args_list[-3:]],
                         ['incident_external_action_unavailable']*3)
        self.assertTrue(all(c.args[2]==503 for c in chain.reject.call_args_list[-3:]))
        first=chain.reject.call_args_list[0]
        self.assertEqual(first.kwargs['user'],None);self.assertEqual(first.args[2:],(401,'unauthorized'))

    def test_boundary_first_red_stops_without_false_summary(self):
        chain=self.bare();chain.reject=mock.Mock(side_effect=AssertionError('first boundary red'))
        with self.assertRaisesRegex(AssertionError,'first boundary red'):chain.boundaries(self.before(),self.body())
        self.assertEqual(chain.reject.call_count,1);chain.marker_case.assert_not_called()

    def test_invalid_request_keys_stay_invalid_after_http_ows_normalization(self):
        chain=self.bare();chain.reject=mock.Mock()
        chain.boundaries(self.before(),self.body())
        keys=[c.kwargs['request_key'] for c in chain.reject.call_args_list if 'request_key' in c.kwargs]
        self.assertEqual(len(keys),4)
        # Go net/textproto trims surrounding SP/HTAB before Header.Get.
        pattern=re.compile(r'[A-Za-z0-9][A-Za-z0-9._:-]{7,127}')
        for key in keys:
            with self.subTest(key=key):
                self.assertIsNone(pattern.fullmatch('' if key is None else key.strip(' \t')))

    def test_missing_key_removes_inherited_case_variants_without_modifying_parent_headers(self):
        chain=self.bare();chain.source_id='source-owned';chain.source_token='synthetic'
        inherited={'Cookie':'own','Idempotency-Key':'inherited-valid-123','idempotency-key':'other-valid-123'}
        seen=[]
        def parent_call(self,user,method,path,*args,**kwargs):
            return incident.ro.transport(incident.ro.BASES[0],method,path,b'{}',inherited)
        def transport(base,verb,path,body,headers,**options):seen.append(dict(headers));return {},{}
        with mock.patch.object(incident.ro,'transport',transport),mock.patch.object(incident.ro.Chain,'call',parent_call):
            chain.call('reader','POST','/api/v1/incidents',{},request_key=None)
            chain.call('reader','POST','/api/v1/incidents',{},request_key='invalid key-123')
        self.assertFalse(any(k.lower()=='idempotency-key' for k in seen[0]))
        self.assertEqual({k:v for k,v in seen[1].items() if k.lower()=='idempotency-key'},
                         {'Idempotency-Key':'invalid key-123'})
        self.assertEqual(inherited['Idempotency-Key'],'inherited-valid-123')

    def test_original_shared_call_and_client_encode_wire_invalid_keys_without_network(self):
        chain=self.bare();chain.source_id='source-owned';chain.source_token='synthetic'
        chain.cookies={'reader':'aiops_session=offline'};chain.calls=0;chain.run_id='offline-wire'
        chain.contract=incident.Contract(incident.ROOT);chain.work=Path('/offline-no-files')
        chain.first_red=mock.Mock();sent=[]
        pattern=re.compile(r'[A-Za-z0-9][A-Za-z0-9._:-]{7,127}')
        base_connection=incident.ro.http.client.HTTPConnection
        class MemorySocket:
            def __init__(self):self.data=[]
            def sendall(self,data):self.data.append(bytes(data))
            def close(self):pass
        class EncodedConnection(base_connection):
            def connect(self):self.sock=MemorySocket()
            def getresponse(self):
                wire=b''.join(self.sock.data);sent.append(wire)
                lines=wire.split(b'\r\n\r\n',1)[0].split(b'\r\n')[1:]
                keys=[line.partition(b':')[2].strip(b' \t').decode() for line in lines
                      if line.partition(b':')[0].lower()==b'idempotency-key']
                valid=len(keys)==1 and pattern.fullmatch(keys[0]) is not None
                response=mock.Mock(status=201 if valid else 422)
                response.read.return_value=json.dumps({'id':'INC-'+'f'*32} if valid else
                                                     {'error':{'code':'idempotency_key_required'}}).encode()
                response.getheaders.return_value=[]
                return response
        with (mock.patch.object(incident.ro.http.client,'HTTPConnection',EncodedConnection),
              mock.patch.object(incident.ro,'append_json'),mock.patch.object(incident.ro,'_private'),
              mock.patch.object(incident.ro,'emit')):
            for key in (None,'short','invalid key-12345678','x'*129):
                chain.call('reader','POST','/api/v1/incidents',self.body(),request_key=key,
                           want=422,code='idempotency_key_required',scenario='boundary')
        self.assertEqual(len(sent),4);chain.first_red.assert_not_called()
        self.assertNotIn(b'Idempotency-Key:',sent[0])
        self.assertIn(b'Idempotency-Key: invalid key-12345678\r\n',sent[2])
        self.assertEqual(chain.calls,4)

    def test_cleanup_always_runs_owned_cleanup_and_never_false_pass(self):
        chain=self.bare();chain.remove_fault=mock.Mock(side_effect=incident.Blocked('fault remove failed'))
        with (mock.patch.object(incident.ro.Chain,'cleanup') as cleanup,
              mock.patch.object(incident.socket,'socket') as sock,mock.patch.object(incident,'emit') as emit):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaises(incident.Blocked):chain.cleanup()
        cleanup.assert_called_once();emit.assert_not_called()

    def test_wire_override_restricted_to_incident_and_restored_after_first_red(self):
        chain=self.bare();chain.source_id='source-owned';chain.source_token='synthetic'
        original=incident.ro.transport
        seen=[]
        def parent_call(self,user,method,path,*args,**kwargs):
            incident.ro.transport(incident.ro.BASES[0],method,path,b'{}',{'Cookie':'own'})
            raise AssertionError('first red')
        def transport(base,verb,path,body,headers,**options):seen.append((path,body,headers));return 422,b'{}',{}
        with mock.patch.object(incident.ro,'transport',transport):
            with mock.patch.object(incident.ro.Chain,'call',parent_call):
                with self.assertRaisesRegex(AssertionError,'first red'):
                    chain.call('reader','POST','/api/v1/incidents',{},request_key=None,raw_body=b'{')
            self.assertIs(incident.ro.transport,transport)
        self.assertIs(incident.ro.transport,original)
        self.assertEqual(seen[0][1],b'{');self.assertNotIn('Idempotency-Key',seen[0][2])

if __name__=='__main__':unittest.main()

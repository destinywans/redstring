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
import api_empty_catalogs_coverage as empty


class EmptyCatalogTests(unittest.TestCase):
    def test_private_lifecycle_does_not_mutate_shared_ports_or_catalogs(self):
        self.assertIsNot(empty.ro, shared)
        self.assertEqual(shared.PORTS, (18602,18603))
        self.assertEqual(empty.ro.PORTS, (18766,18767))
        self.assertEqual(empty.ro.GETS, ())

    def test_opt_in_precedes_lease_doctor_and_resource_acquisition(self):
        with (mock.patch.object(sys,'argv',['empty']),mock.patch.object(empty,'doctor') as doctor,
              mock.patch.object(empty.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit): empty.main()
        doctor.assert_not_called(); opened.assert_not_called()

    def test_exact_inventory_rejects_nonempty_objects_producers_external_and_unreviewed_query(self):
        contract = empty.Contract(empty.ROOT)
        for method,path in (*empty.TARGETS,*empty.SUPPORT): contract.canonical(method,'/api/v1'+path)
        for query in empty.SERVICE_BOUNDARIES: contract.canonical('GET','/api/v1/services?'+query)
        for method,path in [('POST','/api/v1/services'),('GET','/api/v1/services/app'),
            ('GET','/api/v1/mcp-servers/owned'),('POST','/api/v1/ai-extensions'),('GET','/api/v1/changes'),
            ('GET','https://remote.invalid/api/v1/services'),('GET','/api/v1/mcp-servers?q=x'),
            ('GET','/api/v1/services?q=unreviewed'),('GET','/api/v1/services#fragment')]:
            with self.subTest(path=path), self.assertRaises((empty.Blocked,AssertionError)):
                contract.canonical(method,path)

    def test_frozen_input_and_exact_cpu_memory_identity_fail_closed(self):
        data = dict(source_tree_sha=empty.SOURCE_SHA,binary_sha256=empty.BINARY_SHA,runtime_sha=empty.RUNTIME_SHA)
        empty.frozen_identity(data,empty.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError): empty.frozen_identity(dict(data,**{key:'0'*64}),empty.PG_CID)
        with self.assertRaises(AssertionError): empty.frozen_identity(data,'other')
        info = dict(Id=empty.PG_CID,HostConfig=dict(NanoCpus=4_000_000_000,Memory=2<<30))
        with mock.patch.object(empty,'_inspect_pg',return_value=info): empty.inspect_pg('owned')
        for bad in [dict(info,Id='other'),dict(info,HostConfig={}),
                    dict(info,HostConfig=dict(NanoCpus=4_000_000_000,Memory=3<<30))]:
            with mock.patch.object(empty,'_inspect_pg',return_value=bad):
                with self.assertRaises(AssertionError): empty.inspect_pg('owned')

    def test_source_drift_prevents_http(self):
        chain=empty.Chain.__new__(empty.Chain); chain.frozen=mock.Mock(side_effect=empty.Blocked('drift'))
        with mock.patch.object(empty.ro.Chain,'call') as call:
            with self.assertRaises(empty.Blocked): chain.call('reader','GET','/api/v1/services')
        call.assert_not_called()

    def test_empty_mcp_rejects_null_list_fabricated_health_nonzero_counts_and_bool_as_number(self):
        dto = empty.mcp_empty_dto(); empty.verify_mcp(dto)
        for key,value in [('items',None),('items',[{'id':'fabricated'}]),('complete_v3_dto',True),
                          ('complete_v3_dto',0),('unavailable_fields',[])]:
            bad=copy.deepcopy(dto); bad[key]=value
            with self.subTest(key=key,value=value),self.assertRaises(AssertionError): empty.verify_mcp(bad)
        for value in ('1',0,False,None):
            bad=copy.deepcopy(dto); bad['kpis'][2]['value']=value
            with self.assertRaises(AssertionError): empty.verify_mcp(bad)
        bad=dict(dto,connection_health='healthy')
        with self.assertRaises(AssertionError): empty.verify_mcp(bad)

    def test_empty_services_rejects_null_or_nonempty_items_fabricated_metric_and_missing_facet(self):
        dto = empty.services_empty_dto(); empty.verify_services(dto)
        for key,value in [('items',None),('items',[{'id':'fabricated','status':'ok'}]),('facets',[])]:
            bad=copy.deepcopy(dto);bad[key]=value
            with self.assertRaises(AssertionError): empty.verify_services(bad)
        bad=copy.deepcopy(dto);bad['kpis'][0]['value']=0
        with self.assertRaises(AssertionError): empty.verify_services(bad)
        bad=copy.deepcopy(dto);bad['kpis'][4]['value']='1'
        with self.assertRaises(AssertionError): empty.verify_services(bad)

    def test_empty_baseline_and_consistency_reject_any_nonempty_producer_or_read_side_effect(self):
        baseline = {**{name:[] for name in empty.TABLES}, 'audit':[{'id':1}]}
        empty.verify_empty_ledger(baseline); empty.verify_unchanged(baseline,copy.deepcopy(baseline))
        for name in empty.TABLES:
            altered=copy.deepcopy(baseline);altered[name]=[{'id':'untrusted'}]
            with self.subTest(table=name),self.assertRaises(AssertionError): empty.verify_empty_ledger(altered)
            with self.assertRaises(AssertionError): empty.verify_unchanged(baseline,altered)
        altered=copy.deepcopy(baseline);altered['audit'].append({'id':2})
        with self.assertRaises(AssertionError): empty.verify_unchanged(baseline,altered)

    def test_ledger_is_all_business_rows_and_all_audits_read_only(self):
        chain=empty.Chain.__new__(empty.Chain);chain.frozen=mock.Mock();chain.owner=mock.Mock()
        chain.sql=mock.Mock(return_value='{}');chain.ledger();query=chain.sql.call_args.args[0]
        self.assertTrue(chain.sql.call_args.kwargs['read']);self.assertNotIn('WHERE',query)
        self.assertNotIn('DELETE',query);self.assertNotIn('UPDATE',query)
        for name in (*empty.TABLES,'management_audit'): self.assertIn('FROM '+name+' t',query)

    def test_same_validator_applies_to_cross_instance_and_services_require_no_store(self):
        chain=empty.Chain.__new__(empty.Chain);chain.call=mock.Mock(return_value=({}, {'Cache-Control':'no-store'}))
        chain.catalogs(1,scenario='fixture')
        self.assertEqual([call.kwargs['index'] for call in chain.call.call_args_list],[1,1])
        self.assertEqual([call.kwargs['validate'] for call in chain.call.call_args_list],
                         [empty.verify_mcp,empty.verify_services])
        chain.call=mock.Mock(return_value=({}, {}))
        with self.assertRaises(AssertionError): chain.catalogs(0)

    def run_fixture(self):
        chain=empty.Chain.__new__(empty.Chain);chain.cookies={'reader':'memory-cookie-old'}
        chain.calls=20;chain.run_id='a'*32;chain.data={'helper_sha256':{}};chain.database='owned';chain.work=Path('/unused')
        for name in ('provision','start','seed','marker_case','catalogs'):
            setattr(chain,name,mock.Mock())
        def login(actor):
            chain.cookies[actor]='memory-cookie-'+str(chain.login.call_count)
        chain.login=mock.Mock(side_effect=login)
        baseline={**{name:[] for name in empty.TABLES},'audit':[]};chain.ledger=mock.Mock(return_value=baseline)
        chain.call=mock.Mock(return_value=({}, {}))
        return chain

    def test_revocation_is_formal_logout_then_old_cookie_both_apis_and_formal_relogin(self):
        chain=self.run_fixture()
        with mock.patch.object(empty.ro,'_private') as saved: result=chain.run()
        calls=chain.call.call_args_list
        logout=[c for c in calls if c.args[1]=='POST'];self.assertEqual(len(logout),1)
        self.assertEqual(logout[0].args[2],'/api/v1/auth/logout');self.assertEqual(logout[0].kwargs['want'],204)
        at=calls.index(logout[0]);revoked=calls[at+1:]
        self.assertEqual(len(revoked),4)
        self.assertEqual([c.kwargs['index'] for c in revoked],[0,0,1,1])
        self.assertTrue(all(c.kwargs['want']==401 and c.kwargs['code']=='unauthorized' for c in revoked))
        self.assertEqual(chain.login.call_count,2)
        self.assertTrue(result['empty_collection_only']);self.assertFalse(result['nonempty_capabilities_verified'])
        self.assertFalse(result['full_business_chain_verified'])
        self.assertNotIn('cookie',json.dumps(saved.call_args.args[1]))

    def test_mismatch_stops_before_qualification_logout_or_further_requests(self):
        chain=self.run_fixture();chain.catalogs.side_effect=AssertionError('wrong DTO')
        with self.assertRaises(AssertionError): chain.run()
        chain.call.assert_not_called();self.assertEqual(chain.marker_case.call_count,1)

    def test_cleanup_keeps_original_bounded_lifecycle_and_only_checks_owned_ports(self):
        chain=empty.Chain.__new__(empty.Chain)
        with (mock.patch.object(empty.ro.Chain,'cleanup') as cleanup,
              mock.patch.object(empty.socket,'socket') as socket,mock.patch.object(empty,'emit') as emit):
            socket.return_value.__enter__.return_value.connect_ex.return_value=1
            chain.cleanup()
        cleanup.assert_called_once()
        self.assertEqual(socket.return_value.__enter__.return_value.settimeout.call_args_list,[mock.call(.3),mock.call(.3)])
        self.assertEqual(socket.return_value.__enter__.return_value.connect_ex.call_args_list,
                         [mock.call(('127.0.0.1',18766)),mock.call(('127.0.0.1',18767))])
        emit.assert_called_once()

    def test_cleanup_failure_does_not_emit_success(self):
        chain=empty.Chain.__new__(empty.Chain)
        with (mock.patch.object(empty.ro.Chain,'cleanup',side_effect=empty.Blocked('cleanup failed')),
              mock.patch.object(empty.socket,'socket') as socket,mock.patch.object(empty,'emit') as emit):
            with self.assertRaises(empty.Blocked): chain.cleanup()
        socket.assert_not_called();emit.assert_not_called()

    def test_original_outer_execute_cleans_when_business_and_evidence_reporting_fail(self):
        chain=mock.Mock();chain.run.side_effect=RuntimeError('business failed');chain.first_red.side_effect=OSError('evidence failed')
        with mock.patch.object(empty.ro,'emit',side_effect=OSError('stdout failed')):
            with self.assertRaises(RuntimeError): empty.ro.execute(chain)
        chain.cleanup.assert_called_once()


if __name__=='__main__': unittest.main()

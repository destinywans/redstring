import contextlib
import copy
import csv
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_audit_csv_coverage as audit
import discovery_chain as discovery

class AuditCSVTests(unittest.TestCase):
    def fixture(self):
        row = dict(id='7',time='2026-10-05T12:00:00Z',actor_id='a'*32,
                   actor_kind='user',action='v3.service.created',target='b'*32,outcome='success')
        headers = {'Content-Type':'text/csv; charset=utf-8',
                   'Content-Disposition':'attachment; filename="audit.csv"','Cache-Control':'no-store'}
        return [row],headers,self.encode([row])

    def encode(self, rows, columns=None):
        columns = audit.COLUMNS if columns is None else columns
        stream=io.StringIO(newline='');writer=csv.writer(stream);writer.writerow(columns)
        for row in rows: writer.writerow([row[k] for k in columns])
        return stream.getvalue().encode()

    def test_private_helper_ports_inventory_and_no_producer_chain(self):
        self.assertIsNot(audit.ro,shared);self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(audit.PORTS,(18750,18751))
        contract=audit.Contract(audit.ROOT)
        for method,path in (*audit.TARGETS,*audit.SUPPORT):
            contract.canonical(method,'/api/v1'+path)
        for method,path in [('POST','/api/v1/runbooks'),('POST','/api/v1/incidents'),
                            ('POST','/api/v1/audit/export?application=other'),
                            ('GET','/api/v1/audit?limit=10001'),('GET','https://elsewhere/audit'),
                            ('GET','/api/v1/models'),('POST','/api/v1/models/test')]:
            with self.assertRaises((audit.Blocked,AssertionError)):contract.canonical(method,path)

    def test_live_opt_in_before_any_lease_or_doctor(self):
        with (mock.patch.object(sys,'argv',['audit-csv']),mock.patch.object(audit.os,'open') as opened,
              mock.patch.object(audit,'doctor') as doctor,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit):audit.main()
        opened.assert_not_called();doctor.assert_not_called()

    def test_frozen_identity_rejects_all_drift(self):
        data=dict(source_tree_sha=audit.SOURCE_SHA,binary_sha256=audit.BINARY_SHA,runtime_sha=audit.RUNTIME_SHA)
        audit.frozen_identity(data,audit.PG_CID)
        for field in data:
            with self.assertRaises(AssertionError):audit.frozen_identity(dict(data,**{field:'drift'}),audit.PG_CID)
        with self.assertRaises(AssertionError):audit.frozen_identity(data,'other-container')

    def test_csv_exact_rows_and_case_insensitive_security_headers(self):
        rows,headers,raw=self.fixture()
        first=audit.verify_csv(raw,headers,rows,('synthetic-password','hidden-stage-target'))
        self.assertEqual(first,audit.verify_csv(raw,{k.upper():v for k,v in headers.items()},rows,('absent',)))
        for field in audit.COLUMNS:
            bad=copy.deepcopy(rows);bad[0][field]='different'
            with self.subTest(field=field),self.assertRaises(AssertionError):audit.verify_csv(raw,headers,bad,('absent',))

    def test_csv_rejects_hidden_duplicate_missing_and_extra_payload_columns(self):
        rows,headers,raw=self.fixture()
        for records in [[],rows*2,[dict(rows[0],target='hidden-stage-target')]]:
            with self.assertRaises(AssertionError):audit.verify_csv(self.encode(records),headers,rows,('absent',))
        payload=[dict(rows[0],payload='synthetic-password')]
        with self.assertRaises(AssertionError):audit.verify_csv(self.encode(payload,audit.COLUMNS+['payload']),headers,rows,('absent',))
        with self.assertRaises(AssertionError):audit.verify_csv(raw.replace(b',success\r\n',b',success,unexpected\r\n'),headers,rows,('absent',))
        with self.assertRaises(AssertionError):audit.verify_csv(raw.replace(b',success\r\n',b'\r\n'),headers,rows,('absent',))

    def test_csv_rejects_headers_sensitive_data_invalid_utf8_and_unbounded_body(self):
        rows,headers,raw=self.fixture()
        for field,value in [('Content-Type','application/json'),('Content-Disposition','inline'),('Cache-Control','public')]:
            with self.assertRaises(AssertionError):audit.verify_csv(raw,dict(headers,**{field:value}),rows,('absent',))
        for sensitive in [('',),('v3.service.created',),('a'*32,)]:
            with self.assertRaises(AssertionError):audit.verify_csv(raw,headers,rows,sensitive)
        for invalid in [b'',raw+b'\x00',b'x'*65537]:
            with self.assertRaises(AssertionError):audit.verify_csv(invalid,headers,rows,('absent',))
        with self.assertRaises(UnicodeDecodeError):audit.verify_csv(b'\xff',headers,rows,('absent',))

    def test_csv_quoted_commas_and_newlines_are_parsed_as_exact_cells(self):
        rows,headers,_=self.fixture();rows[0]['action']='synthetic,quoted\nvalue'
        audit.verify_csv(self.encode(rows),headers,rows,('absent',))

    def test_existing_discovery_does_not_check_csv_time_but_new_validator_rejects_drift(self):
        # Reuse the real original validator offline. This is an assertion-gap
        # counterexample, not a product failure or reason to rerun Discovery.
        rows,headers,_=self.fixture();rows[0].update(action='runbook.created',outcome='succeeded')
        rows += [dict(rows[0],id='8',action='runbook.submitted'),
                 dict(rows[0],id='9',action='runbook.review.approved')]
        changed=copy.deepcopy(rows);changed[0]['time']='2099-01-01T00:00:00Z';raw=self.encode(changed)
        chain=discovery.Chain.__new__(discovery.Chain)
        chain.export_url='/api/v1/audit/exports/audit-'+'c'*36
        chain.cookies={'reader':'synthetic-owner'};chain.ids={'runbook':rows[0]['target']}
        chain.contract=shared.Contract(audit.ROOT);chain.calls=0;chain.run_id='01234567-89ab-4cde-8123-0123456789ab'
        chain.work=Path('/private/tmp/offline-discovery-csv');chain.first_red=mock.Mock()
        expected={row['id']:{'actor':row['actor_id'],'action':row['action']} for row in rows}
        with (mock.patch.object(discovery.readonly,'transport',return_value=(200,raw,headers)),
              mock.patch.object(discovery.readonly,'private_file'),
              mock.patch.object(discovery.readonly,'append_json'),mock.patch.object(discovery,'emit')):
            chain.download_csv(expected,rows[0]['target'])
        with self.assertRaisesRegex(AssertionError,'times/actors'):audit.verify_csv(raw,headers,rows,('absent',))

    def bare(self):
        chain=audit.Chain.__new__(audit.Chain)
        chain.export_url='/api/v1/audit/exports/audit-'+'c'*36;chain.cookies={'reader':'aiops_session=synthetic'}
        chain.run_id='01234567-89ab-4cde-8123-0123456789ab';chain.calls=0
        chain.contract=audit.Contract(audit.ROOT);chain.work=Path('/private/tmp/offline-audit-csv')
        chain.frozen=mock.Mock();chain.first_red=mock.Mock()
        chain.restore_pending=True;chain.roles={'reader':'readonly-'+'a'*32+'-reader'}
        chain.database='aiops_testlab_readonly_'+'a'*32;chain.db_user='readonly_'+'a'*32
        chain.owner_tag='testlab:exact';chain.owner=mock.Mock();chain.pg=mock.Mock(side_effect=['','1'])
        return chain

    def test_download_uses_exact_second_api_owner_and_can_omit_environment_header(self):
        chain=self.bare();rows,headers,raw=self.fixture()
        with (mock.patch.object(audit.ro,'transport',return_value=(200,raw,headers)) as transport,
              mock.patch.object(audit.ro,'private_file'),mock.patch.object(audit.ro,'append_json'),
              mock.patch.object(audit,'emit')):
            chain.download_csv(rows,('absent',),index=1,environment=None)
        args,kwargs=transport.call_args
        self.assertEqual(args,(audit.ro.BASES[1],'GET',chain.export_url))
        self.assertEqual(kwargs['headers'],{'Origin':audit.ro.BASES[1],'Cookie':chain.cookies['reader']})
        self.assertEqual(chain.calls,1)

    def test_download_first_red_no_retry_and_reporting_failures_cannot_mask_contract(self):
        chain=self.bare();rows,headers,raw=self.fixture()
        with (mock.patch.object(audit.ro,'transport',return_value=(200,raw,dict(headers,**{'Cache-Control':'public'}))) as transport,
              mock.patch.object(audit.ro,'append_json',side_effect=OSError('report disk full')),
              mock.patch.object(audit,'emit',side_effect=BrokenPipeError('report pipe closed'))):
            with self.assertRaisesRegex(AssertionError,'security/attachment'):chain.download_csv(rows,('absent',),index=0,environment='test')
        self.assertEqual(transport.call_count,1);chain.first_red.assert_called_once()

    def test_read_ledger_is_compared_after_each_get_and_denial(self):
        chain=self.bare();before=dict(audit=[{'id':7}],exports=[{'id':'audit-owned','csv_data':'bytes'}])
        chain.ledger=mock.Mock(return_value=copy.deepcopy(before));request=mock.Mock(return_value={'id':7})
        self.assertEqual(chain.read_without_write(before,request,'reader'),{'id':7})
        for field in ['audit','exports']:
            changed=copy.deepcopy(before);changed[field][0]['changed']='timestamp-or-data'
            chain.ledger.return_value=changed
            with self.assertRaisesRegex(AssertionError,'complete persisted ledger'):chain.read_without_write(before,request,'reader')

    def test_restore_owner_insert_readback_share_one_four_second_deadline_and_no_frozen(self):
        chain=self.bare()
        with mock.patch.object(audit.time,'monotonic',return_value=100):chain.restore_grant()
        self.assertFalse(chain.restore_pending);chain.frozen.assert_not_called()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual([call.kwargs['deadline'] for call in chain.pg.call_args_list],[104,104])
        for call in chain.pg.call_args_list:self.assertEqual(call.args[1:],(chain.database,chain.db_user))
        self.assertIn('WHERE NOT EXISTS',chain.pg.call_args_list[0].args[0])
        self.assertTrue(chain.pg.call_args_list[1].args[0].startswith('BEGIN READ ONLY;'))

    def test_restore_bad_readback_stays_pending_and_expires_before_native_inspection(self):
        chain=self.bare();chain.pg.side_effect=['','2']
        with self.assertRaisesRegex(AssertionError,'exactly restored'):chain.restore_grant()
        self.assertTrue(chain.restore_pending)
        chain.owner=lambda deadline:audit.Chain.owner(chain,deadline=deadline)
        chain.pg=lambda *a,**k:audit.ro.Chain.pg(chain,*a,**k)
        with (mock.patch.object(audit.time,'monotonic',side_effect=[100,105]),
              mock.patch.object(audit.ro,'inspect_pg') as inspect,mock.patch.object(audit.ro.subprocess,'run') as native):
            with self.assertRaisesRegex(audit.Blocked,'cleanup exhausted'):chain.restore_grant()
        inspect.assert_not_called();native.assert_not_called()

    def test_restore_failure_still_runs_original_cleanup_and_does_not_claim_green(self):
        chain=self.bare();chain.restore_grant=mock.Mock(side_effect=audit.Blocked('restore failed'))
        with mock.patch.object(audit.ro.Chain,'cleanup') as original,mock.patch.object(audit,'emit') as emit:
            with self.assertRaisesRegex(audit.Blocked,'restore failed'):chain.cleanup()
        original.assert_called_once_with();emit.assert_not_called()

    def test_exact_original_grant_only_no_other_account_or_environment_restore(self):
        role='readonly-'+'a'*32+'-reader';remove,restore,read=audit.grant_statements(role)
        for text in (remove,restore,read):
            self.assertIn("role_id='"+role+"'",text);self.assertIn("scope_id='test'",text)
            self.assertIn("module='audit'",text);self.assertIn("action='view'",text)
        for role in ['reader','readonly-'+'a'*32+'-peer',"readonly-';DROP"]:
            with self.assertRaises(AssertionError):audit.grant_statements(role)

    def flow(self):
        chain=self.bare();rows,_,_=self.fixture();rows[0]['id']='1'
        native=[]
        for identifier,environment,target in [(1,'test','b'*32),(2,'staging','d'*32)]:
            native.append(dict(id=identifier,action='v3.service.created',actor_kind='user',
                               outcome='success',target=target,changes={'environment':environment}))
        before=dict(audit=native,exports=[])
        after=copy.deepcopy(before);after['exports']=[dict(id=chain.export_url.rsplit('/',1)[1],
            owner_id='e'*32,environment_ids=['test'],includes_global=False,csv_data='preserved')]
        chain.users={'reader':'e'*32};chain.nonce='a'*32;chain.marker='private-display-marker'
        chain.password='synthetic-private-password';chain.db_password='synthetic-db-password'
        chain.admin_email='synthetic@example.invalid';chain.restore_pending=False
        chain.marker_case=mock.Mock();chain.ledger=mock.Mock(side_effect=[before]*3+[after]*8)
        chain.sql=mock.Mock(side_effect=[json.dumps(rows),'f'*64,'1',''])
        chain.download_csv=mock.Mock(return_value='f'*64);chain.restore_grant=mock.Mock()
        def request(user,method,path,*args,**kwargs):
            value = {'items':[{'id':'1'}]} if path=='/api/v1/audit' else {'id':'1','chain_ok':True}
            if path=='/api/v1/audit/export': value={'url':chain.export_url}
            if 'validate' in kwargs:kwargs['validate'](value)
            return value,{}
        chain.call=mock.Mock(side_effect=request)
        return chain,before,after

    def test_complete_flow_keeps_original_authorization_contract_and_new_anon_read_proof(self):
        chain,_,_=self.flow();chain.audits()
        requests=chain.call.call_args_list
        self.assertEqual([(c.args[0],c.kwargs.get('environment','test'),c.kwargs.get('want',200))
                          for c in requests if c.args[1]=='GET' and c.args[2]==chain.export_url],
                         [(None,'test',401),('peer','test',404),('reader','staging',403),('reader','test',403)])
        self.assertEqual([c.kwargs for c in chain.download_csv.call_args_list],
                         [dict(index=1,environment='test'),dict(index=0,environment=None),dict(index=1,environment=None)])
        self.assertEqual([c.args[0] for c in chain.marker_case.call_args_list],
                         [x[len('audit_csv.'):] for x in audit.REQUIRED_CASES if x!='audit_csv.cleanup'])
        self.assertEqual(chain.ledger.call_count,11)

    def test_wrong_anonymous_response_stops_at_first_red_without_revoke_or_retry(self):
        chain,_,_=self.flow();original=chain.call.side_effect
        def response(*args,**kwargs):
            if args[0] is None:raise AssertionError('anonymous actual200 expected401')
            return original(*args,**kwargs)
        chain.call.side_effect=response
        with self.assertRaisesRegex(AssertionError,'anonymous actual200'):chain.audits()
        self.assertEqual(sum(c.args[0] is None for c in chain.call.call_args_list),1)
        self.assertFalse(chain.restore_pending);chain.restore_grant.assert_not_called()
        self.assertNotIn('anonymous',[c.args[0] for c in chain.marker_case.call_args_list])

    def test_unknown_revoke_commit_outcome_sets_restore_pending_before_sql(self):
        chain,_,_=self.flow();chain.sql.side_effect=[json.dumps(self.fixture()[0]).replace('"7"','"1"'),'f'*64,'1',
                                                                 audit.Blocked('revoke outcome unknown')]
        with self.assertRaisesRegex(audit.Blocked,'revoke outcome unknown'):chain.audits()
        self.assertTrue(chain.restore_pending);chain.restore_grant.assert_not_called()
        self.assertNotIn('revoked',[c.args[0] for c in chain.marker_case.call_args_list])

if __name__=='__main__':unittest.main()

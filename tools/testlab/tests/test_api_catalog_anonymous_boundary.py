"""Offline bounds/counterexamples; never acquires a native test resource."""
import contextlib
import copy
import hashlib
import io
import json
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_catalog_anonymous_boundary as c

DTO = {'error': {'code': 'unauthorized', 'message': 'Login required'}}

class CatalogAnonymousTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.work = Path(self.tmp.name).resolve()

    def chain(self):
        x = c.Chain.__new__(c.Chain)
        x.nonce = 'a'*32
        x.work, x.run_id, x.users, x.cookies, x.calls = self.work, str(uuid.uuid4()), {}, {}, 0
        x.contract, x.frozen, x.first_red = c.Contract(c.ROOT), Mock(), Mock()
        x.marker_case = Mock()
        return x

    def wire(self, x, status=401, value=DTO, headers=None):
        fake = Mock(return_value=(status, json.dumps(value).encode(), headers or {'Content-Type': 'application/json'}))
        with patch.object(c.ro, 'transport', fake), patch.dict(c.h._namespace, transport=fake), contextlib.redirect_stdout(io.StringIO()) as out:
            result = x.call(None, 'GET', '/api/v1/agents/lifecycle', want=401, code='unauthorized', scenario='authorization', index=1)
            self.assertIs(c.ro.transport, fake)
            self.assertIs(c.h._namespace['transport'], fake)
        return result, fake, [json.loads(line) for line in out.getvalue().splitlines()]

    def test_real_call_uses401_authorization_false_registration_and_safe_wire(self):
        x = self.chain()
        _, fake, events = self.wire(x)
        self.assertEqual(x.calls, 1)
        self.assertEqual(fake.call_args.args[0], 'http://127.0.0.1:18927')
        self.assertFalse(any(k.lower() in ('cookie', 'authorization') for k in fake.call_args.args[4]))
        receipt = json.loads((self.work/'anonymous-wire-1.json').read_text())
        self.assertEqual(receipt['response_body'], DTO)
        self.assertEqual(receipt['response_sha256'], hashlib.sha256(json.dumps(DTO).encode()).hexdigest())
        self.assertEqual(receipt['API_index'], 1)
        self.assertFalse(receipt['credentials_stored'])
        self.assertTrue(all(not event['interface']['registration_evidence'] for event in events))
        x.first_red.assert_not_called()

    def test_wrong_status_code_dto_contenttype_cannot_pass_or_store_raw_sensitive(self):
        samples = [(200, DTO, None), (403, DTO, None), (401, {'error': {'code':'forbidden','message':'Login required'}}, None),
                   (401, {'error': {'code':'unauthorized','message':'secret-unsafe-response'}}, None),
                   (401, dict(DTO, cookie='secret-unsafe-response'), None), (401, DTO, {'Content-Type':'text/event-stream'})]
        for status, value, headers in samples:
            x = self.chain()
            with self.assertRaises(AssertionError):
                self.wire(x, status, value, headers)
            x.first_red.assert_called_once()
            self.assertFalse((self.work/'anonymous-wire-1.json').exists())
            self.assertNotIn('secret-unsafe-response', (self.work/'interfaces.jsonl').read_text())

    def test_scope_excludes_public_instance_login_mutations_and_queries(self):
        contract = c.Contract(c.ROOT)
        for method, path in c.TARGETS:
            self.assertEqual(contract.canonical(method, '/api/v1'+path.format(id='a'*32,node='root',type='gitlab'))[1], 'main')
        for method, path in (('GET','/api/v1/instance'), ('POST','/api/v1/auth/login'), ('PUT','/api/v1/prompts'), ('GET','/api/v1/prompts?cursor=1')):
            with self.assertRaises((AssertionError, c.Blocked)):
                contract.canonical(method, path)

    def test_cookie_or_authenticated_user_rejected_before_transport(self):
        for user, cookies in (('reader', {}), (None, {'reader':'never-store-cookie'})):
            x = self.chain()
            x.cookies = cookies
            with patch.object(c.ro, 'transport') as transport, self.assertRaises(AssertionError):
                x.call(user,'GET','/api/v1/prompts',want=401,code='unauthorized',scenario='authorization')
            transport.assert_not_called()

    def test_two_ledger_snapshots_and_fortysix_calls_no_seed_login(self):
        x = self.chain()
        before = {'sessions':'a'*64,'management_audit':'b'*64,'all_other':'c'*64}
        x.provision, x.start = Mock(), Mock()
        x.ledger = Mock(return_value=before)
        def call(*args, **kwargs):
            x.calls += 1
            self.assertIsNone(args[0])
            self.assertEqual(kwargs['want'], 401)
            self.assertEqual(kwargs['scenario'], 'authorization')
        x.call = Mock(side_effect=call)
        result = x.run()
        self.assertEqual(x.ledger.call_count, 2)
        self.assertEqual(x.call.call_count, 46)
        self.assertEqual({v.kwargs['index'] for v in x.call.call_args_list}, {0,1})
        self.assertEqual(x.marker_case.call_count, 25)
        self.assertEqual(result['normal_added'], 0)
        self.assertFalse(result['counts_as_whole_acceptance'])
        for v in x.call.call_args_list:
            self.assertNotIn('body', v.kwargs)

    def test_any_public_digest_or_table_presence_change_fails_final_ledger(self):
        original = {'sessions':'a'*64,'management_audit':'b'*64,'arbitrary_public':'c'*64}
        for key in original:
            x = self.chain()
            changed = copy.deepcopy(original)
            changed[key] = 'd'*64
            x.provision, x.start = Mock(), Mock()
            x.ledger = Mock(side_effect=[original,changed])
            def call(*args, **kwargs): x.calls += 1
            x.call = Mock(side_effect=call)
            with self.assertRaises(AssertionError): x.run()
            self.assertFalse((self.work/'anonymous-result.json').exists())

    def test_full_ledger_includes_sessions_audit_and_row_digests_no_count(self):
        x = self.chain()
        tables = ['management_audit','sessions','users']
        hashes = {t:'a'*64 for t in tables}
        x.sql = Mock(side_effect=[json.dumps(tables),json.dumps(hashes)])
        with patch.object(c.h.Chain, 'owner') as owner:
            self.assertEqual(x.ledger(), hashes)
            owner.assert_called_once_with(x)
        self.assertTrue(all(v.kwargs['read'] for v in x.sql.call_args_list))
        statement = x.sql.call_args_list[1].args[0]
        self.assertIn('jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)', statement)
        self.assertNotIn('count(', statement.lower())
        for missing in ('sessions','management_audit'):
            x.sql = Mock(return_value=json.dumps([t for t in tables if t != missing]))
            with patch.object(c.h.Chain, 'owner'), self.assertRaises(AssertionError): x.ledger()

    def test_evidence_failure_restores_transport_no_retry(self):
        x = self.chain()
        fake = Mock(return_value=(401,json.dumps(DTO).encode(),{'Content-Type':'application/json'}))
        with patch.object(c.ro,'transport',fake), patch.dict(c.h._namespace,transport=fake), patch.object(c.ro,'_private',side_effect=OSError('offline evidence fault')), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(AssertionError): x.call(None,'GET','/api/v1/prompts',want=401,code='unauthorized',scenario='authorization')
            self.assertIs(c.ro.transport,fake)
            self.assertIs(c.h._namespace['transport'],fake)
        fake.assert_called_once()
        x.first_red.assert_called_once()

    def test_confirmed_writes_are_empty_json_and_no_object_lookup_evidence(self):
        for method, path in (('POST','/api/v1/investigations/'+'a'*32+'/branches'), ('PATCH','/api/v1/investigations/'+'a'*32+'/nodes/root')):
            x=self.chain()
            fake=Mock(return_value=(401,json.dumps(DTO).encode(),{'Content-Type':'application/json'}))
            with patch.object(c.ro,'transport',fake), patch.dict(c.h._namespace,transport=fake), contextlib.redirect_stdout(io.StringIO()) as out:
                x.call(None,method,path,{},want=401,code='unauthorized',scenario='authorization')
            self.assertEqual(fake.call_args.args[3], b'{}')
            self.assertEqual(fake.call_args.args[4]['Content-Type'],'application/json')
            self.assertTrue(all(not json.loads(line)['interface']['registration_evidence'] for line in out.getvalue().splitlines()))
            with patch.object(c.ro,'transport') as transport, self.assertRaises(AssertionError):
                x.call(None,method,path,{'arbitrary':'forbidden'},want=401,code='unauthorized',scenario='authorization')
            transport.assert_not_called()

    def test_inventory_is_pure_and_cleanup_delegates_sole40(self):
        with patch.object(c,'doctor') as doctor, contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(c.main(['--inventory']),0)
        doctor.assert_not_called()
        result=json.loads(out.getvalue())
        self.assertEqual(result['estimated_HTTP_calls'],46)
        self.assertEqual(result['cleanup_seconds'],40)
        self.assertEqual(result['grace_seconds'],45)
        self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
        x=self.chain()
        with patch.object(c.ro.Chain,'cleanup') as cleanup, patch.object(c.socket,'socket') as sock, contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            x.cleanup()
        cleanup.assert_called_once_with(x)

if __name__ == '__main__': unittest.main()

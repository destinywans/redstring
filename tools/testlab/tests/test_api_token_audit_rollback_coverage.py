import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_token_audit_rollback_coverage as token


class TokenAuditTests(unittest.TestCase):
    def test_marker_emission_with_actual_shared_constructor_fixture_label(self):
        run_id = str(uuid.uuid4())
        data = dict(owner=token.ro.OWNER, binary_sha256=token.BINARY_SHA,
                    source_tree_sha=token.SOURCE_SHA)
        with (tempfile.TemporaryDirectory() as directory,
              mock.patch.object(token.ro, 'Contract'), mock.patch.object(token, 'Contract')):
            chain = token.Chain(Path(directory), data, run_id)
            self.assertEqual(chain.marker, 'rofixture' + uuid.UUID(run_id).hex)
            chain.frozen = mock.Mock()
            with mock.patch.object(token, 'emit') as emit:
                chain.marker_case('native_issue_consume', {'tokens': 1})
            emit.assert_called_once_with('token_audit.native_issue_consume', 'passed',
                                         {'run_id': run_id, 'tokens': 1})

    def fixture(self):
        row = dict(id='a'*32, owner_id='b'*32, environment_id='test', name='Owned',
                   scopes=['connectors:view'], digest='c'*64, created_at='start',
                   expires_at='later', revoked_at=None)
        empty = dict(tokens=[], audit=[])
        issued = dict(tokens=[row], audit=[dict(action='v3.api_token.create', target=row['id'], actor_id='b'*32,
                                               outcome='success', changes=dict(environment_id='test', scopes=['connectors:view'], ttl_seconds=3600))])
        revoked = copy.deepcopy(issued)
        revoked['tokens'][0]['revoked_at'] = 'now'
        revoked['audit'].append(dict(action='v3.api_token.revoke', target=row['id'], actor_id='b'*32,
                                      outcome='success', changes=dict(environment_id='test')))
        return empty, issued, revoked

    def test_private_import_preserves_shared_module(self):
        self.assertIsNot(shared, token.ro)
        self.assertEqual(shared.PORTS, (18602, 18603))
        self.assertEqual(token.ro.PORTS, (18738, 18739))

    def test_opt_in_before_doctor_lock_or_resources(self):
        with mock.patch.object(token, 'doctor') as doctor, mock.patch.object(token.os, 'open') as opened, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit): token.main([])
        doctor.assert_not_called(); opened.assert_not_called()

    def test_inventory_rejects_other_writes_queries_and_external_origin(self):
        c = token.Contract(token.ROOT)
        for method, path in (*token.TARGETS, *token.SUPPORT):
            c.canonical(method, '/api/v1'+path.replace('{id}', 'a'*32))
        for method, path in [('POST','/api/v1/services'), ('GET','/api/v1/connectors?x=1'),
                             ('GET','https://remote.invalid/api/v1/connectors')]:
            with self.assertRaises((AssertionError, token.Blocked)): c.canonical(method, path)

    def test_identity_and_exact_pg_fail_closed(self):
        data = dict(source_tree_sha=token.SOURCE_SHA, binary_sha256=token.BINARY_SHA, runtime_sha=token.RUNTIME_SHA)
        token.frozen_identity(data, token.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError): token.frozen_identity(dict(data, **{key:'0'*64}), token.PG_CID)
        with mock.patch.object(token, '_inspect_pg', return_value={'Id':'foreign'}):
            with self.assertRaises(AssertionError): token.ro.inspect_pg('owned')

    def test_fault_scopes_and_no_injected_identifiers(self):
        for action in ('v3.api_token.create', 'v3.api_token.revoke'):
            name, create, drop = token.fault_statements('a'*32,'b'*32, action)
            self.assertIn("NEW.action='"+action+"'", create)
            self.assertIn("NEW.actor_id='"+'b'*32+"'", create)
            self.assertIn("NEW.changes->>'environment_id'='test'", create)
            self.assertIn('DROP TRIGGER IF EXISTS '+name+' ON public.management_audit', drop)
            self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()', drop)
        for args in [('x','b'*32,'v3.api_token.create'), ('a'*32,"';DROP TABLE users;",'v3.api_token.create'), ('a'*32,'b'*32,'v3.other')]:
            with self.assertRaises(AssertionError): token.fault_statements(*args)

    def test_complete_issue_and_revoke_transitions(self):
        empty, issued, revoked = self.fixture()
        token.verify_transition(empty, issued, 'a'*32, 'v3.api_token.create')
        token.verify_transition(issued, revoked, 'a'*32, 'v3.api_token.revoke')

    def test_partial_hash_state_and_duplicate_audit_rejected(self):
        _, issued, revoked = self.fixture()
        for changed in ('digest','owner_id','expires_at','scopes'):
            bad = copy.deepcopy(revoked); bad['tokens'][0][changed] = 'foreign'
            with self.assertRaises(AssertionError): token.verify_transition(issued, bad, 'a'*32, 'v3.api_token.revoke')
        bad = copy.deepcopy(revoked); bad['audit'].append(bad['audit'][-1])
        with self.assertRaises(AssertionError): token.verify_transition(issued, bad, 'a'*32, 'v3.api_token.revoke')
        bad = copy.deepcopy(revoked); bad['tokens'][0]['revoked_at'] = None
        with self.assertRaises(AssertionError): token.verify_transition(issued, bad, 'a'*32, 'v3.api_token.revoke')

    def test_issue_recovery_preserves_previous_token_hash(self):
        _, issued, _ = self.fixture(); after = copy.deepcopy(issued)
        new = dict(issued['tokens'][0], id='d'*32); after['tokens'].append(new)
        after['audit'].append(dict(issued['audit'][0], target='d'*32))
        token.verify_transition(issued, after, 'd'*32, 'v3.api_token.create')
        after['tokens'][0]['digest'] = 'e'*64
        with self.assertRaises(AssertionError): token.verify_transition(issued, after, 'd'*32, 'v3.api_token.create')

    def test_secret_scrub_nested_fields_and_free_text_before_emit(self):
        secret = 'aiops_pat_'+'s'*43
        value = dict(secret_shown_once=secret, secret_hash='c'*64, nested=[dict(Authorization=secret)], message='echo '+secret)
        clean = token.safe(value)
        self.assertNotIn(secret, json.dumps(clean)); self.assertNotIn('c'*64, json.dumps(clean))
        with contextlib.redirect_stdout(io.StringIO()) as output: token.emit('test','failed', value)
        self.assertNotIn(secret, output.getvalue()); self.assertNotIn('c'*64, output.getvalue())

    def request_chain(self):
        chain = token.Chain.__new__(token.Chain)
        chain.frozen = mock.Mock(); chain.contract = mock.Mock()
        chain.contract.canonical.return_value = ('/api/v1/api-tokens','main')
        chain.cookies = {'reader':'synthetic-cookie'}; chain.calls = 0; chain.run_id='run'
        chain.work = Path('/private/tmp/not-created-token-offline')
        chain.first_red = mock.Mock()
        return chain

    def test_success_secret_only_returned_in_memory_not_evidence(self):
        chain = self.request_chain(); secret='aiops_pat_'+'s'*43
        with mock.patch.object(token.ro, 'transport', return_value=(201,json.dumps(dict(id='a'*32,secret_shown_once=secret)).encode(),{})), mock.patch.object(token.ro, 'append_json') as append, contextlib.redirect_stdout(io.StringIO()) as stdout:
            result,_ = chain.request('POST','/api/v1/api-tokens',{},want=201)
        self.assertEqual(result['secret_shown_once'],secret)
        self.assertNotIn(secret,repr(append.call_args)); self.assertNotIn(secret,stdout.getvalue())

    def test_failed_response_and_transport_never_persist_raw_secret(self):
        secret='aiops_pat_'+'s'*43
        for response in ((201,json.dumps(dict(secret_shown_once=secret)).encode(),{}), None):
            chain=self.request_chain()
            with mock.patch.object(token.ro, 'transport', side_effect=RuntimeError(secret) if response is None else None, return_value=response), mock.patch.object(token.ro, 'append_json') as append, contextlib.redirect_stdout(io.StringIO()) as stdout:
                with self.assertRaises(AssertionError): chain.request('POST','/api/v1/api-tokens',{},want=503)
            self.assertNotIn(secret,repr(append.call_args)); self.assertNotIn(secret,repr(chain.first_red.call_args)); self.assertNotIn(secret,stdout.getvalue())

    def test_drift_and_bearer_write_stop_before_transport(self):
        chain=self.request_chain(); chain.frozen.side_effect=token.Blocked('drift')
        with mock.patch.object(token.ro,'transport') as transport:
            with self.assertRaises(token.Blocked): chain.request('GET','/api/v1/connectors')
        transport.assert_not_called()
        chain.frozen.side_effect=None
        with mock.patch.object(token.ro,'transport') as transport:
            with self.assertRaises(AssertionError): chain.request('POST','/api/v1/api-tokens',bearer='private')
        transport.assert_not_called()

    def test_cleanup_held_lifecycle_runs_even_fault_restore_fails(self):
        chain=self.request_chain(); chain.remove_fault=mock.Mock(side_effect=AssertionError('restore failed'))
        with mock.patch.object(token.ro.Chain,'cleanup') as cleanup, mock.patch.object(token.socket,'socket') as sock:
            sock.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaises(AssertionError): chain.cleanup()
        cleanup.assert_called_once()

    def test_fault_restore_owner_drop_and_readback_share_one_bounded_deadline(self):
        chain = self.request_chain()
        chain.fault_attempted = True
        chain.nonce = 'b'*32
        chain.users = {'reader': 'a'*32}
        chain.database, chain.db_user, chain.owner_tag = 'own-db', 'own-role', 'own-marker'
        chain.pg = mock.Mock(side_effect=[
            json.dumps(dict(database='own-db', role='own-role', db_tag='own-marker', role_tag='own-marker')),
            '', json.dumps(dict(trigger=False, function=False))])
        with mock.patch.object(token.time, 'monotonic', return_value=100):
            chain.remove_fault()
        self.assertEqual(chain.pg.call_count, 3)
        self.assertTrue(all(call.kwargs['deadline'] == 104 and call.args[1:] == ('own-db', 'own-role')
                            for call in chain.pg.call_args_list))
        self.assertFalse(chain.fault_attempted)

    def test_restore_deadline_failure_keeps_unknown_fault_and_runs_original_cleanup(self):
        chain = self.request_chain()
        chain.fault_attempted = True
        chain.nonce = 'b'*32
        chain.users = {'reader': 'a'*32}
        chain.database, chain.db_user, chain.owner_tag = 'own-db', 'own-role', 'own-marker'
        chain.pg = mock.Mock(side_effect=token.Blocked('restore deadline exceeded'))
        with (mock.patch.object(token.ro.Chain, 'cleanup') as cleanup,
              mock.patch.object(token.socket, 'socket') as sock):
            sock.return_value.__enter__.return_value.connect_ex.return_value = 1
            with self.assertRaises(token.Blocked):
                chain.cleanup()
        cleanup.assert_called_once()
        self.assertTrue(chain.fault_attempted)


if __name__ == '__main__': unittest.main()

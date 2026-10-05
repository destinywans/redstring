import contextlib
import copy
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import sys
import ssl
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_connectors_mapping_functional_coverage as mapping


class MappingCoverageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        data = dict(source_tree_sha=mapping.SOURCE_SHA, binary_sha256=mapping.BINARY_SHA,
                    helper_sha256=mapping.hashes(), vault_root_token='root-secret',
                    postgres_container='owned')
        root = Path(self.temp.name).resolve()
        for relative in ('internal/apicontract/openapi.json', 'docs/api/v3-contract.md'):
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((mapping.ROOT / relative).read_bytes())
        self.chain = mapping.Chain(root, data, str(uuid.uuid4()))
        self.candidate = dict(id='candidate', version='2026-10-01T00:00:00Z', service_id='app',
                              environment_id='test', system_id='code', binding_revision=1,
                              service='svc', connector_id='connector', target_id='target',
                              external_id='101', expires_at='2026-10-01T00:15:00Z')

    def test_inventory_is_readonly_two_missing_targets(self):
        with patch.object(mapping, 'doctor') as doctor, contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(mapping.main(['--inventory']), 0)
        data = json.loads(out.getvalue())
        self.assertEqual(len(data['targets']), 2)
        self.assertEqual(data['ports'], [18644, 18645, 18646, 18647])
        self.assertEqual(data['evidence_mode'], 'protocol_mock')
        doctor.assert_not_called()

    def test_contract_rejects_delivery_external_and_nonformal_paths(self):
        for method, path in [('POST', '/api/v1/service-mappings/accept'), ('POST', '/api/v1/service-mappings/discover')]:
            self.assertEqual(self.chain.contract.canonical(method, path)[1], 'main')
        for method, path in [('POST', '/api/v1/changes'), ('POST', 'https://external/api/v1/service-mappings/accept'),
                             ('GET', '/service-mappings'), ('POST', '/api/v1/connectors/x/execute')]:
            with self.assertRaises(mapping.Blocked):
                self.chain.contract.canonical(method, path)

    def test_port_and_emitter_context_restores_on_exception(self):
        old = mapping.readonly.PORTS, mapping.readonly.BASES, mapping.readonly.emit
        with self.assertRaises(RuntimeError), mapping.context():
            self.assertEqual(mapping.readonly.PORTS, mapping.PORTS)
            raise RuntimeError('controlled')
        self.assertEqual((mapping.readonly.PORTS, mapping.readonly.BASES, mapping.readonly.emit), old)

    def test_work_and_role_owned_by_fresh_uuid(self):
        self.assertIn('/.local/testlab/connectors-parallel/', str(self.chain.work))
        self.assertTrue(self.chain.database.endswith(self.chain.nonce))
        self.assertTrue(self.chain.db_user.endswith(self.chain.nonce))
        self.assertEqual((self.chain.work / 'credentials.json').stat().st_mode & 0o777, 0o600)

    def test_vault_guard_rejects_wrong_owner_image_cid_and_port(self):
        info = dict(Name='/aiops-' + mapping.OWNER + '-vault', Id='c' * 64, Image='sha256:' + 'd' * 64,
                    Config=dict(Image=mapping.VAULT_IMAGE, Labels={'aiops.testlab.owner': mapping.OWNER}),
                    State=dict(Running=True, StartedAt='now'),
                    NetworkSettings=dict(Ports={'8200/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18572'}]}))
        identity = mapping.guard_vault(info)
        for mutation in [lambda d: d.update(Id='invalid'), lambda d: d['Config'].update(Image='latest'),
                         lambda d: d['Config']['Labels'].update({'aiops.testlab.owner': 'foreign'}),
                         lambda d: d['NetworkSettings']['Ports']['8200/tcp'][0].update(HostIp='0.0.0.0')]:
            value = copy.deepcopy(info)
            mutation(value)
            with self.assertRaises(mapping.Blocked):
                mapping.guard_vault(value, identity)

    def test_exact_pg_cid_before_any_sql(self):
        with patch.object(mapping.readonly, 'inspect_pg', return_value={'Id': 'foreign'}), \
                patch.object(mapping.readonly.Chain, 'pg') as pg, self.assertRaises(mapping.Blocked):
            self.chain.pg('SELECT 1')
        pg.assert_not_called()

    def test_no_mock_write_forwarding_unknown_or_credential_bypass(self):
        protocol = mapping.GitLabProtocol('exact-secret')
        for method, path, token, status in [('POST', '/api/v4/projects/101', 'exact-secret', 405),
                                           ('GET', 'https://foreign/api/v4/projects/101', 'exact-secret', 404),
                                           ('GET', '/api/v4/projects/999', 'exact-secret', 404),
                                           ('GET', '/api/v4/projects/101', 'wrong-secret', 401)]:
            self.assertEqual(protocol.dispatch(method, path, token)[0], status)
        self.assertEqual(protocol.snapshot(), [])
        self.assertEqual(protocol.dispatch('GET', '/api/v4/projects/101', 'exact-secret')[1]['id'], 101)
        self.assertNotIn('exact-secret', json.dumps(protocol.snapshot()))

    def test_mock_failures_and_wrong_project_preserve_exact_receipt(self):
        protocol = mapping.GitLabProtocol('token')
        for mode in ('503', '429', 'wrong_project', 'timeout', 'response_loss'):
            protocol.mode = mode
            status, value, actual_mode = protocol.dispatch('GET', '/api/v4/projects/101', 'token')
            self.assertEqual(actual_mode, mode)
            self.assertEqual(status, int(mode) if mode in ('503', '429') else 200)
            if mode == 'wrong_project':
                self.assertEqual(value['id'], 102)
        self.assertEqual(len(protocol.snapshot()), 5)

    def test_nonempty_discovery_rejects_scope_revision_and_source_drift(self):
        expected = {'app': dict(service='svc', connector_id='connector', target_id='target',
                                external_id='101', binding_revision=1)}
        good = dict(source_state='configured', suggestions=1, candidates=[self.candidate])
        self.assertEqual(mapping.validate_discovery(good, expected), [self.candidate])
        for field, value in [('environment_id', 'staging'), ('connector_id', 'foreign'),
                             ('target_id', 'other'), ('binding_revision', 2), ('external_id', '102')]:
            bad = copy.deepcopy(good)
            bad['candidates'][0][field] = value
            with self.assertRaises(AssertionError):
                mapping.validate_discovery(bad, expected)
        with self.assertRaises(AssertionError):
            mapping.validate_discovery(dict(source_state='configured', suggestions=0, candidates=[]), expected)

    def test_accept_exact_dto_and_idempotent_status(self):
        candidate = self.candidate
        item = dict(candidate_id='candidate', service_id='app', environment_id='test', system_id='code',
                    connector_id='connector', target_id='target', external_id='101', binding_revision=2,
                    binding=dict(kind='gitlab', connector_id='connector', resource='101'), status='accepted')
        good = dict(accepted=1, items=[item])
        mapping.validate_accept(good, [candidate], 'connector')
        for mutate in [lambda v: v.update(accepted=0), lambda v: v['items'][0].update(binding_revision=3),
                       lambda v: v['items'][0]['binding'].update(resource='102'),
                       lambda v: v['items'][0].update(status='already_accepted')]:
            value = copy.deepcopy(good)
            mutate(value)
            with self.assertRaises(AssertionError):
                mapping.validate_accept(value, [candidate], 'connector')
        duplicate = copy.deepcopy(good)
        duplicate['accepted'] = 0
        duplicate['items'][0]['status'] = 'already_accepted'
        mapping.validate_accept(duplicate, [candidate], 'connector', False)

    def test_target_no_validator_rejected_before_http(self):
        with patch.object(mapping.readonly.Chain, 'call') as call, self.assertRaises(mapping.Blocked):
            self.chain.call('peer', 'POST', '/api/v1/service-mappings/discover')
        call.assert_not_called()

    def test_protocol_evidence_cannot_accidentally_count_real_gitlab(self):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            mapping.emit('test', 'passed', {}, evidence_mode='real_local',
                         interface=dict(method='POST', path='/api/v1/service-mappings/discover'))
        row = json.loads(output.getvalue())
        self.assertEqual(row['evidence_mode'], 'protocol_mock')
        self.assertEqual(row['interface']['evidence_mode'], 'protocol_mock')

    def test_exact_audit_fault_is_not_business_success_seed(self):
        text = self.chain.audit_fault('v3.service_mapping.accepted', 'env', 'app')
        self.assertIn('NEW.actor_id=' + mapping.q(self.chain.users['peer']), text)
        self.assertIn("NEW.application_id='app'", text)
        self.assertIn("NEW.target='env'", text)
        self.assertNotIn('INSERT INTO', text)
        self.assertNotIn('UPDATE ', text)

    def test_cleanup_attempts_vault_after_pg_error(self):
        self.chain.vault_tokens = ['ephemeral-token']
        self.chain.vault_policies = ['owned-policy']
        self.chain.server = Mock()
        with patch.object(mapping.readonly.Chain, 'cleanup', side_effect=mapping.Blocked('PG fault')), \
                patch.object(self.chain, 'vault') as vault, \
                patch.object(mapping.socket, 'socket') as sock, self.assertRaises(mapping.Blocked):
            sock.return_value.__enter__.return_value.connect_ex.return_value = 1
            self.chain.cleanup()
        self.chain.server.close.assert_called_once()
        self.assertEqual(vault.call_count, 3)

    def offline_vault(self, chain, method, path, *args, **kwargs):
        if path == '/v1/auth/token/create':
            return {'auth': {'client_token': 'ephemeral-token'}}
        if method == 'GET' and not path.endswith('-sibling'):
            return {'data': {'data': {'token': chain.secret}}}
        return {}

    def prepare_offline(self, chain):
        def server(protocol, cert, key):
            # Keep real certificate/key loading without opening a listener.
            ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER).load_cert_chain(cert, key)
            self.assertEqual(Path(cert), chain.work / 'tls-cert.pem')
            self.assertEqual(Path(key), chain.work / 'tls-key.pem')
            return Mock()
        with patch.object(chain, 'vault', side_effect=lambda *a, **kw: self.offline_vault(chain, *a, **kw)), \
                patch.object(chain, 'marker_case'), patch.object(mapping, 'GitLabServer', side_effect=server), \
                patch.object(mapping.readonly, 'private_json', side_effect=AssertionError('shared config access')):
            chain.prepare_vault()

    def test_expired_shared_tls_is_not_reused(self):
        shared = self.chain.root / 'shared-component'
        shared.mkdir()
        cert, key = shared / 'tls-cert.pem', shared / 'tls-key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '1',
                        '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost',
                        '-out', str(cert), '-keyout', str(key)], check=True, capture_output=True, timeout=20)
        subprocess.run(['openssl', 'x509', '-in', str(cert), '-signkey', str(key), '-days', '0',
                        '-out', str(cert)], check=True, capture_output=True, timeout=20)
        self.assertLess(ssl.cert_time_to_seconds(ssl._ssl._test_decode_cert(str(cert))['notAfter']),
                        datetime.now(timezone.utc).timestamp())
        original = cert.read_bytes(), key.read_bytes()
        self.chain.data['component_work'] = str(shared)
        self.prepare_offline(self.chain)
        own = self.chain.work / 'tls-cert.pem'
        decoded = ssl._ssl._test_decode_cert(str(own))
        self.assertGreater(ssl.cert_time_to_seconds(decoded['notAfter']), datetime.now(timezone.utc).timestamp() + 86400)
        self.assertIn(('DNS', 'localhost'), decoded['subjectAltName'])
        subprocess.run(['openssl', 'verify', '-CAfile', str(own), '-verify_hostname', 'localhost', str(own)],
                       check=True, capture_output=True, timeout=20)
        self.assertNotEqual(own.read_bytes(), original[0])
        self.assertEqual((cert.read_bytes(), key.read_bytes()), original)

    def test_uuid_tls_identity_permissions_and_exact_trust_without_shared_config(self):
        other = mapping.Chain(self.chain.root, dict(self.chain.data), str(uuid.uuid4()))
        identities = []
        for chain in (self.chain, other):
            self.prepare_offline(chain)
            cert, key = chain.work / 'tls-cert.pem', chain.work / 'tls-key.pem'
            identities.append((cert.read_bytes(), key.read_bytes()))
            for path in (cert, key):
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertFalse(path.is_symlink())
            chain.binding = [{'owned': True}]
            self.assertEqual(chain.startup_environment()['SSL_CERT_FILE'], str(cert))
        self.assertNotEqual(identities[0][0], identities[1][0])
        self.assertNotEqual(identities[0][1], identities[1][1])

    def test_doctor_keeps_vault_admission_without_shared_tls_files(self):
        work = self.chain.root / '.local/testlab/environments' / mapping.OWNER
        component = dict(owner=mapping.OWNER, work=str(work), credentials={'vault_token': 'root-secret'})
        with patch.object(mapping, 'ports_free'), \
                patch.object(mapping.readonly, 'doctor', return_value=self.chain.data), \
                patch.object(mapping.readonly, 'inspect_pg', return_value={'Id': mapping.PG_CID}), \
                patch.object(mapping.readonly, 'private_json', return_value=component), \
                patch.object(mapping, 'guard_vault', return_value={'owned': True}):
            admitted = mapping.doctor(self.chain.root)
        self.assertEqual(admitted['vault_root_token'], 'root-secret')
        self.assertEqual(admitted['source_tree_sha'], mapping.SOURCE_SHA)
        self.assertEqual(admitted['binary_sha256'], mapping.BINARY_SHA)
        self.assertFalse(work.exists())

    def test_tls_generation_failure_still_cleans_provisioned_vault(self):
        chain = self.chain
        with patch.object(chain, 'vault', side_effect=lambda *a, **kw: self.offline_vault(chain, *a, **kw)) as vault, \
                patch.object(chain, 'marker_case'), patch.object(mapping, 'GitLabServer') as server, \
                patch.object(mapping.subprocess, 'run', return_value=Mock(returncode=1)), \
                patch.object(mapping.readonly.Chain, 'cleanup') as cleanup, \
                patch.object(mapping.socket, 'socket') as sock, contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(AssertionError, 'TLS certificate generation failed'):
                chain.prepare_vault()
            sock.return_value.__enter__.return_value.connect_ex.return_value = 1
            chain.cleanup()
        server.assert_not_called()
        cleanup.assert_called_once()
        vault.assert_any_call('POST', '/v1/auth/token/revoke', {'token': 'ephemeral-token'}, want=204)
        vault.assert_any_call('DELETE', '/v1/secret/metadata/' + chain.vault_path, want=204)
        vault.assert_any_call('DELETE', '/v1/sys/policies/acl/mapping-' + chain.run_id, want=204)


if __name__ == '__main__':
    unittest.main()

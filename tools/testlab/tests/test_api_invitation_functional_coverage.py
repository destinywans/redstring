"""Offline invitation TLS/capture/native linkage guards; no real listeners."""
import base64
import copy
import contextlib
import io
import importlib
import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
inv = importlib.import_module('api_invitation_functional_coverage')


class ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.p = inv.SMTPProtocol('a' * 32, 'synthetic-user', 'synthetic-password')
        self.email = self.p.address('normal')

    def test_recipient_is_exact_nonce_and_declared_scenario(self):
        self.assertTrue(self.p.allowed(self.email))
        for email in ('user@example.com', 'normal-' + 'b' * 32 + '@testlab.invalid',
                      'unknown-' + 'a' * 32 + '@testlab.invalid', self.email + '\r\nRCPT TO:real@example.com'):
            self.assertFalse(self.p.allowed(email))

    def test_authentication_is_exact_and_never_logged(self):
        valid = base64.b64encode(b'\0synthetic-user\0synthetic-password').decode()
        self.assertTrue(self.p.authenticate(valid))
        for value in ('invalid', base64.b64encode(b'foreign\0synthetic-user\0synthetic-password').decode()):
            self.assertFalse(self.p.authenticate(value))
        self.assertEqual(self.p.captures, [])

    def test_invalid_mode_and_foreign_recipient_do_not_capture(self):
        with self.assertRaises(inv.Blocked): self.p.capture('real@example.com', b'body')
        self.p.mode = 'unknown'
        with self.assertRaises(inv.Blocked): self.p.capture(self.email, b'body')
        self.assertEqual(self.p.captures, [])

    def test_acceptance_is_recorded_before_lost_response(self):
        self.p.mode = 'lose_data_response'; outcome = self.p.capture(self.email, b'synthetic body')
        self.assertEqual(outcome, 'accepted_lost_response')
        self.assertEqual(self.p.captures[0]['recipient'], self.email)
        self.assertNotIn('password', self.p.captures[0])

    def test_privileged_smtp_port_denial_is_environment_blocked_without_adopting_server(self):
        sink=inv.SMTPSink(self.p,Path('private-cert'),Path('private-key'))
        with patch.object(inv.ssl,'SSLContext') as tls,patch.object(inv.socketserver.ThreadingTCPServer,'__init__',side_effect=PermissionError(13,'Permission denied')):
            with self.assertRaises(inv.Blocked):sink.start()
        self.assertIsNone(sink.server);self.assertIsNone(sink.thread)

    def test_protocol_constructor_and_sink_constructor_do_not_listen(self):
        with patch.object(inv.socket, 'socket', side_effect=AssertionError('listener acquired')):
            inv.SMTPProtocol('b' * 32, 'user', 'secret')
            inv.SMTPSink(self.p, Path('private-cert'), Path('private-key'))

    def test_parse_code_and_expiry_from_native_mail(self):
        code = base64.urlsafe_b64encode(b'x' * 32).decode().rstrip('=')
        message = ('From: ' + inv.FROM + '\r\nTo: ' + self.email + '\r\nSubject: =?utf-8?q?Redstring_=E9=82=80=E8=AF=B7_/_Redstring_invitation?=\r\n'
            'Content-Type: text/plain; charset=utf-8\r\n\r\nTrusted product entry: ' + inv.ORIGIN + '/accept-invitation\r\n'
            'One-time invitation code: ' + code + '\r\nCode expires at: 2026-10-02T00:00:00Z\r\n').encode()
        value = inv.mail_evidence(dict(recipient=self.email, message=message.decode(), outcome='accepted'))
        self.assertEqual(value['token_hash'], inv.hashlib.sha256(b'x' * 32).hexdigest())
        self.assertEqual(value['code'], code)
        for bad in (message.replace(inv.ORIGIN.encode(), b'https://foreign.invalid'), message.replace(code.encode(), b'invalid-code')):
            with self.assertRaises(AssertionError): inv.mail_evidence(dict(recipient=self.email, message=bad.decode(), outcome='accepted'))


class DockerCaptureTests(unittest.TestCase):
    def test_cleanup_accepts_exact_lowercase_absence_but_never_foreign_or_daemon_error(self):
        with tempfile.TemporaryDirectory() as work:
            p=inv.ContainerProtocol('a'*32,'user','secret',Path(work)/'output')
            for stderr,expected in (('error: no such object: '+ 'c'*64,True),('error: no such object: foreign',False),('daemon unavailable',False)):
                sink=inv.SMTPDockerSink(p,Path(work),'11111111-1111-4111-8111-111111111111');sink.cid='c'*64
                sink.guard=unittest.mock.Mock();sink.docker=unittest.mock.Mock()
                result=unittest.mock.Mock(returncode=1,stdout='[]\n',stderr=stderr)
                with patch.object(inv.subprocess,'run',return_value=result):
                    if expected:sink.close();self.assertIsNone(sink.cid)
                    else:
                        with self.assertRaises(AssertionError):sink.close()

    def test_private_capture_rejects_foreign_recipient_mode_and_large_output(self):
        with tempfile.TemporaryDirectory() as work:
            p=inv.ContainerProtocol('a'*32,'user','secret',Path(work));path=Path(work)/'captures.jsonl'
            inv.readonly.private_file(path,json.dumps(dict(sender=inv.FROM,recipient=p.address('normal'),outcome='accepted',message='body',size=4,at=1))+'\n')
            self.assertEqual(len(p.captures),1)
            path.write_text(json.dumps(dict(sender=inv.FROM,recipient='foreign@testlab.invalid',outcome='accepted',message='body'))+'\n')
            with self.assertRaises(inv.Blocked):_ = p.captures
            with self.assertRaises(inv.Blocked):p.mode='foreign'

    def test_container_guard_rejects_swapped_identity_mount_resource_and_port(self):
        with tempfile.TemporaryDirectory() as work:
            p=inv.ContainerProtocol('a'*32,'user','secret',Path(work)/'output')
            sink=inv.SMTPDockerSink(p,Path(work),'11111111-1111-4111-8111-111111111111');sink.cid='c'*64
            info=sink.expected_inspect_fixture()
            sink.validate(info)
            for key in ('Id','Image','Name'):
                bad=copy.deepcopy(info);bad[key]='foreign'
                with self.assertRaises(inv.Blocked):sink.validate(bad)
            for mutation in (lambda x:x['Config']['Labels'].update({'testlab.invitation.run':'foreign'}),
                lambda x:x['HostConfig'].update({'NanoCpus':1000000000}),
                lambda x:x['HostConfig']['PortBindings']['465/tcp'][0].update({'HostIp':'0.0.0.0'}),
                lambda x:x['Mounts'][0].update({'RW':False})):
                bad=copy.deepcopy(info);mutation(bad)
                with self.assertRaises(inv.Blocked):sink.validate(bad)


class NativeTests(unittest.TestCase):
    def fixture(self, state='delivered'):
        protocol = inv.SMTPProtocol('a' * 32, 'user', 'secret'); email = protocol.address('normal')
        code = base64.urlsafe_b64encode(b'x' * 32).decode().rstrip('=')
        native = dict(id='b' * 32, email=email, role='viewer', environment_id='test', inviter_id='c' * 32,
            token_hash=inv.hashlib.sha256(b'x' * 32).hexdigest(), status=state, expires_at='2026-10-02T00:00:00+00:00',
            delivered_at='2026-10-01T00:00:00+00:00' if state in ('delivered', 'accepted') else None,
            account_id=None, accepted_at=None)
        audit = [dict(actor='c' * 32, action=inv.RELAY_ACTION, target='b' * 32, outcome='success', changes={'environment': 'test'})] if state=='delivered' else []
        mail = dict(code=code, recipient=email, token_hash=native['token_hash'], expires_at='2026-10-02T00:00:00Z', outcome='accepted')
        return native, audit, mail

    def test_delivered_exact_actor_scope_code_expiry_and_audit(self):
        native, audit, mail = self.fixture()
        inv.validate_invitation(native, audit, mail, 'c' * 32, 'delivered')
        for key in ('id', 'email', 'role', 'environment_id', 'inviter_id', 'token_hash', 'expires_at'):
            with self.subTest(key=key):
                bad = dict(native); bad[key] = 'foreign'
                with self.assertRaises(AssertionError): inv.validate_invitation(bad, audit, mail, 'c' * 32, 'delivered')

    def test_native_unknown_does_not_become_delivered(self):
        for state in ('pending', 'superseded'):
            native, audit, mail = self.fixture(state)
            inv.validate_invitation(native, audit, mail, 'c' * 32, state)
            with self.assertRaises(AssertionError): inv.validate_invitation(native, [dict(actor='c' * 32, action=inv.RELAY_ACTION,
                target=native['id'], outcome='success', changes={'environment': 'test'})], mail, 'c' * 32, state)

    def test_audit_swap_wrong_actor_and_scope_fail(self):
        native, audit, mail = self.fixture()
        for field in ('actor', 'action', 'target', 'outcome'):
            bad = copy.deepcopy(audit); bad[0][field] = 'foreign'
            with self.assertRaises(AssertionError): inv.validate_invitation(native, bad, mail, 'c' * 32, 'delivered')
        bad = copy.deepcopy(audit); bad[0]['changes']['environment'] = 'staging'
        with self.assertRaises(AssertionError): inv.validate_invitation(native, bad, mail, 'c' * 32, 'delivered')
        extra=copy.deepcopy(audit);extra.append(dict(actor='foreign',action='foreign',target='foreign',outcome='success',changes={}))
        with self.assertRaises(AssertionError):inv.validate_invitation(native,extra,mail,'c'*32,'delivered')

    def test_audit_rejection_ddl_is_actor_email_action_conditional(self):
        email = 'audit-failure-' + 'a' * 32 + '@testlab.invalid'
        text = inv.fault_ddl('a' * 32, 'b' * 32, email)
        self.assertIn(inv.RELAY_ACTION, text); self.assertIn(email, text)
        self.assertIn("NEW.actor_id='" + 'b' * 32, text)
        self.assertIn('i.id=NEW.target', text); self.assertIn("i.environment_id='test'", text)
        self.assertNotIn('INSERT INTO', text); self.assertNotIn('UPDATE ', text)
        with self.assertRaises(inv.Blocked): inv.fault_ddl('a' * 32, 'b' * 32, 'real@example.com')

    def test_stable_scope_and_markers(self):
        self.assertEqual(inv.API_PORTS, (18624,18625)); self.assertEqual(inv.HTTPS_PORT, 18626)
        self.assertEqual(inv.SMTP_PORT, 465)
        self.assertEqual(len(inv.REQUIRED_CASES), len(set(inv.REQUIRED_CASES)))
        self.assertEqual(inv.TARGETS, ('POST /api/v1/members/invitations',))


class ChainGuards(unittest.TestCase):
    def make(self, work):
        chain=inv.Chain.__new__(inv.Chain);chain.work=Path(work);chain.calls=0
        chain.run_id='11111111-1111-4111-8111-111111111111';chain.nonce=inv.uuid.UUID(chain.run_id).hex
        chain.database='aiops_testlab_readonly_'+chain.nonce;chain.db_user='readonly_'+chain.nonce
        chain.owner_tag=inv.readonly.OWNER+':'+chain.run_id+':api-readonly'
        chain.protocol=inv.SMTPProtocol(chain.nonce,'user','secret');chain.users={'reader':'a'*32,'peer':'b'*32}
        chain.cookies={'reader':'aiops_session=private-session'};chain.protocol_files={}
        chain.contract=unittest.mock.Mock();chain.contract.canonical.return_value=('/api/v1/members/invitations','main')
        chain.http_records={};chain.qualified=set();chain.data={'source_tree_sha':'d'*64}
        chain.frozen=lambda:None;chain.first_red=lambda row:None;return chain

    def response(self, status, payload, cookie=''):
        response=unittest.mock.Mock();response.status=status;response.read.return_value=json.dumps(payload).encode()
        response.getheaders.return_value=[('Set-Cookie',cookie)] if cookie else []
        response.getheader.return_value=cookie;connection=unittest.mock.Mock();connection.getresponse.return_value=response
        return connection

    def test_http_unknown_route_actor_and_scope_rejected_before_connection(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work)
            with patch.object(inv.http.client,'HTTPSConnection',side_effect=AssertionError('connection acquired')):
                for change in ({'path':'/other'},{'user':'foreign'},{'index':2},{'environment':'production'}):
                    args=dict(user='reader',method='POST',path='/api/v1/members/invitations');args.update(change)
                    with self.assertRaises(inv.Blocked):chain.call(**args)

    def test_http_error_validates_native_semantics_and_records_real_local_scope(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);connection=self.response(503,{'error':{'code':'invitation_unavailable'}});validated=[]
            with patch.object(inv.ssl,'create_default_context') as tls,patch.object(inv.http.client,'HTTPSConnection',return_value=connection),contextlib.redirect_stdout(io.StringIO()):
                chain.call('reader','POST','/api/v1/members/invitations',want=503,code='invitation_unavailable',scenario='failure',validate=lambda value:validated.append(value))
            self.assertEqual(len(validated),1);connection.close.assert_called_once();tls.assert_called_once_with(cafile=str(chain.work/'invitation-cert.pem'))
            text=(chain.work/'interfaces.jsonl').read_text();row=json.loads(text)
            self.assertEqual(row['evidence_mode'],'real_local');self.assertFalse(row['registration_evidence'])
            self.assertIn('external mailbox',row['proof_scope']);self.assertNotIn('private-session',text)

    def test_wrong_native_success_is_not_registered(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);connection=self.response(200,{'ok':False})
            with patch.object(inv.ssl,'create_default_context'),patch.object(inv.http.client,'HTTPSConnection',return_value=connection),contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(AssertionError):chain.call('reader','POST','/api/v1/members/invitations',validate=lambda value:inv.check(value=={'ok':True},'wrong native success'))
            row=json.loads((chain.work/'interfaces.jsonl').read_text());self.assertEqual(row['status'],'failed');self.assertFalse(row['registration_evidence'])

    def test_http_2xx_waits_for_native_proof_and_wrong_native_audit_never_qualifies(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);connection=self.response(200,{'ok':True})
            with patch.object(inv.ssl,'create_default_context'),patch.object(inv.http.client,'HTTPSConnection',return_value=connection),contextlib.redirect_stdout(io.StringIO()):
                chain.invite('normal')
            original=json.loads((chain.work/'interfaces.jsonl').read_text())
            self.assertFalse(original['registration_evidence'])
            native,audit,mail=NativeTests().fixture();native['email']=chain.protocol.address('normal');native['inviter_id']=chain.users['reader']
            before=dict(invitations=[],users=[],workspace={},audit=[])
            chain.ledger=lambda:dict(before,invitations=[native],audit=audit)
            message='From: '+inv.FROM+'\r\nTo: '+native['email']+'\r\n\r\nTrusted product entry: '+inv.ORIGIN+'/accept-invitation\r\nOne-time invitation code: '+mail['code']+'\r\nCode expires at: '+mail['expires_at']+'\r\n'
            chain.protocol.captures=[dict(recipient=native['email'],message=message,outcome='accepted')]
            with self.assertRaises(AssertionError):chain.proof(before,native['email'],'delivered')
            self.assertFalse(any(json.loads(line)['registration_evidence'] for line in (chain.work/'interfaces.jsonl').read_text().splitlines()))

    def test_native_matched_proof_qualifies_exact_call_only_once_and_rejects_swap(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);chain.data={'source_tree_sha':'d'*64};connection=self.response(200,{'ok':True})
            with patch.object(inv.ssl,'create_default_context'),patch.object(inv.http.client,'HTTPSConnection',return_value=connection),contextlib.redirect_stdout(io.StringIO()):
                chain.invite('normal')
            call_id=chain.run_id+'-1';proof=dict(run_id=chain.run_id,database=chain.database,source_tree_sha='d'*64,http_call_id=call_id,
                verified=True,invitation_id='b'*32,email=chain.protocol.address('normal'),actor=chain.users['reader'],environment='test',
                token_hash='c'*64,kind='invitation',status='delivered')
            bad=dict(proof,email='foreign@testlab.invalid')
            with self.assertRaises(AssertionError):chain.qualify(call_id,bad,chain.work/'bad.json')
            artifact=chain.work/'native.json';inv.readonly._private(artifact,proof)
            with contextlib.redirect_stdout(io.StringIO()):chain.qualify(call_id,proof,artifact)
            rows=[json.loads(line) for line in (chain.work/'interfaces.jsonl').read_text().splitlines()]
            self.assertFalse(rows[0]['registration_evidence']);self.assertTrue(rows[1]['registration_evidence'])
            self.assertEqual(rows[1]['original_call_id'],call_id);self.assertEqual(rows[1]['proof']['invitation_id'],'b'*32)
            self.assertEqual(rows[1]['response_sha256'],chain.http_records[call_id]['response_sha256'])
            with self.assertRaises(AssertionError):chain.qualify(call_id,proof,artifact)

    def test_https_login_requires_secure_and_httponly_cookie(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);connection=self.response(200,{'user':{}},'aiops_session=secret; Path=/; HttpOnly')
            with patch.object(inv.ssl,'create_default_context'),patch.object(inv.http.client,'HTTPSConnection',return_value=connection),contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(AssertionError):chain.call('reader','POST','/api/v1/auth/login',scenario='fixture')

    def test_listener_preflight_never_adopts_existing_465(self):
        socket=unittest.mock.MagicMock();socket.__enter__.return_value=socket;socket.connect_ex.side_effect=[1,1,1,0]
        with patch.object(inv.socket,'socket',return_value=socket):
            with self.assertRaises(inv.Blocked):inv.ports_free()

    def test_private_lease_is_independent_and_exclusive(self):
        with tempfile.TemporaryDirectory() as root,patch.object(inv.readonly,'exclusive',side_effect=AssertionError('shared lease used')):
            with inv.exclusive(root):
                with self.assertRaises(inv.Blocked):
                    with inv.exclusive(root):pass

    def test_fault_guard_precedes_any_mutation(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);chain.fault_guard=unittest.mock.Mock(side_effect=inv.Blocked('wrong CID'));chain.sql=unittest.mock.Mock()
            with self.assertRaises(inv.Blocked):chain.install_fault()
            chain.sql.assert_not_called()

    def test_guard_rejects_changed_nonce_role_owner_and_container(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);chain.data={'postgres_container':'owned-pg','postgres_image_id':'sha256:owned','postgres_container_id':'c'*64}
            catalog=dict(owner=chain.db_user,db_tag=chain.owner_tag,role_tag=chain.owner_tag);chain.pg=lambda text:json.dumps(catalog)
            with patch.object(inv.readonly,'inspect_pg',return_value={'Id':'c'*64}),patch.object(inv.readonly,'guard_pg',side_effect=lambda value,*args:value):
                chain.fault_guard()
                original=chain.nonce;chain.nonce='f'*32
                with self.assertRaises(inv.Blocked):chain.fault_guard()
                chain.nonce=original;catalog['role_tag']='foreign'
                with self.assertRaises(inv.Blocked):chain.fault_guard()
            with patch.object(inv.readonly,'inspect_pg',return_value={'Id':'f'*64}),patch.object(inv.readonly,'guard_pg',side_effect=lambda value,*args:value):
                with self.assertRaises(inv.Blocked):chain.fault_guard()

    def test_cleanup_still_drops_owned_db_if_trigger_cleanup_errors(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);chain.fault_installed=True;chain.smtp=chain.relay=None
            chain.remove_fault=unittest.mock.Mock(side_effect=inv.Blocked('guard rejected'))
            with patch.object(inv.readonly.Chain,'cleanup') as cleanup:
                with self.assertRaises(inv.Blocked):chain.cleanup()
                cleanup.assert_called_once()
            self.assertEqual(json.loads((chain.work/'smtp-captures.json').read_text())['captures'],[])

    def test_frozen_private_inputs_reject_hash_mode_or_symlink(self):
        with tempfile.TemporaryDirectory() as work:
            chain=self.make(work);del chain.frozen;path=chain.work/'smtp-password';path.write_bytes(b'private');inv.os.chmod(path,0o600)
            chain.protocol_files={path.name:inv.hashlib.sha256(path.read_bytes()).hexdigest()}
            with patch.object(inv.readonly.Chain,'frozen'):
                chain.frozen();inv.os.chmod(path,0o644)
                with self.assertRaises(inv.Blocked):chain.frozen()
                inv.os.chmod(path,0o600);path.write_bytes(b'changed')
                with self.assertRaises(inv.Blocked):chain.frozen()
                path.unlink();path.symlink_to(chain.work/'other')
                with self.assertRaises(inv.Blocked):chain.frozen()


if __name__ == '__main__': unittest.main()

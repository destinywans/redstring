import contextlib
import io
import json
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_content_interfaces_coverage as content


class ContentInterfaceTests(unittest.TestCase):
    def test_private_lifecycle_does_not_replace_shared_ports(self):
        self.assertIsNot(content.ro, shared)
        self.assertEqual(shared.PORTS, (18602, 18603))
        self.assertEqual(content.ro.PORTS, (18628, 18629))

    def test_inventory_rejects_adjacent_business_chain(self):
        contract = content.Contract(content.ROOT)
        for method, path in content.TARGETS:
            concrete = path.replace('{id}', 'INC-test')
            self.assertEqual(contract.canonical(method, '/api/v1'+concrete)[0], '/api/v1'+path)
        for method, path in [('POST', '/incidents/INC-test/status'), ('POST', '/postmortems'),
                             ('POST', '/runbooks'), ('POST', '/uploads'),
                             ('POST', '/investigation-templates/x/start'),
                             ('POST', '/canvases/CV-x/nodes/root/messages')]:
            with self.assertRaises(content.Blocked):
                contract.canonical(method, '/api/v1'+path)
        with self.assertRaises(content.Blocked):
            contract.canonical('GET', 'https://external.invalid/api/v1/canvas-templates')

    def test_source_binary_and_container_each_fail_closed(self):
        data = dict(source_tree_sha=content.SOURCE_SHA, binary_sha256=content.BINARY_SHA)
        content.frozen_identity(data, content.PG_CID)
        for changed, cid in [(dict(data, source_tree_sha='0'*64), content.PG_CID),
                             (dict(data, binary_sha256='0'*64), content.PG_CID),
                             (data, '0'*64)]:
            with self.assertRaises(AssertionError): content.frozen_identity(changed, cid)

    def test_live_opt_in_fails_before_resource_acquisition(self):
        with (mock.patch.object(sys, 'argv', ['content']), mock.patch.object(content, 'doctor') as doctor,
              mock.patch.object(content.os, 'open') as opened, contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit): content.main()
        doctor.assert_not_called(); opened.assert_not_called()

    def test_cleanup_failure_never_announces_pass(self):
        chain = content.Chain.__new__(content.Chain)
        with (mock.patch.object(content.ro.Chain, 'cleanup', side_effect=content.Blocked('unknown DB outcome')),
              mock.patch.object(content, 'emit') as emit, mock.patch.object(content.socket, 'socket') as socket):
            with self.assertRaises(content.Blocked): chain.cleanup()
        emit.assert_not_called(); socket.assert_not_called()

    def test_source_headers_are_exact_and_restored_after_failure(self):
        chain = content.Chain.__new__(content.Chain)
        chain.source_id = 'content-test'; chain.source_token = 'private-synthetic'; chain.nonce = 'a'*32
        original = content.ro.transport
        captured = []
        def parent_call(user, method, path, *args, **kwargs):
            return content.ro.transport('http://127.0.0.1:18628', method, path, b'{}', {'Origin':'local'})
        def transport(*args, **kwargs):
            captured.append(args); raise RuntimeError('bounded failure')
        with (mock.patch.object(content.ro.Chain, 'call', side_effect=parent_call),
              mock.patch.object(content.ro, 'transport', side_effect=transport) as patched):
            for user, path in [(None, '/api/v1/webhooks/alertmanager/content-test'),
                               ('reader', '/api/v1/incidents'), ('reader', '/api/v1/canvas-templates')]:
                with self.assertRaises(RuntimeError): chain.call(user, 'POST', path, {})
                self.assertIs(content.ro.transport, patched)
        self.assertIs(content.ro.transport, original)
        self.assertEqual(captured[0][4]['Authorization'], 'Bearer private-synthetic')
        self.assertNotIn('Authorization', captured[1][4])
        self.assertEqual(captured[1][4]['Idempotency-Key'], 'content-'+'a'*32+'-incident')
        self.assertNotIn('Authorization', captured[2][4]); self.assertNotIn('Idempotency-Key', captured[2][4])

    def test_audit_fault_targets_only_owned_actor_and_two_actions(self):
        chain = content.Chain.__new__(content.Chain); chain.users={'reader':'owned-user'}
        chain.sql = mock.Mock(); chain.audit_fault(True)
        sql = chain.sql.call_args.args[0]
        self.assertIn("NEW.actor_id='owned-user'", sql)
        self.assertIn("NEW.action IN ('v3.canvas_template.saved','incident.timeline.append')", sql)
        self.assertNotIn('INSERT INTO', sql); self.assertNotIn('UPDATE ', sql)
        chain.audit_fault(False)
        self.assertFalse(chain.fault)
        self.assertIn('DROP TRIGGER', chain.sql.call_args.args[0])

    def test_manual_timeline_requires_received_actor_text_and_time(self):
        value=dict(text='real note', actor={'kind':'person'}, time='2026-10-05T00:00:00Z')
        content.timeline_assert(value, 'real note')
        for patch in [dict(text='wrong'), dict(actor={'kind':'agent'}), dict(time=None)]:
            with self.assertRaises(AssertionError): content.timeline_assert(dict(value, **patch), 'real note')

    def test_incident_fixture_disables_every_external_option(self):
        value=content.incident_payload('app', 'alert', 'title')
        self.assertEqual(value['services'], ['app']); self.assertEqual(value['alert_ids'], ['alert'])
        for name in ('war_room','notify','status_page','open_canvas'): self.assertIs(value[name], False)

    def test_named_markers_work_with_inherited_string_marker(self):
        chain=content.Chain.__new__(content.Chain)
        chain.marker='native-input-title'; chain.run_id='owned-run'
        chain.frozen=mock.Mock()
        with mock.patch.object(content, 'emit') as emit:
            for name in content.REQUIRED_CASES: chain.marker_case(name, {'verified':True})
            with self.assertRaises(AssertionError): chain.marker_case('unexpected', {})
        self.assertEqual(emit.call_count, len(content.REQUIRED_CASES))

    def test_view_only_peer_denied_before_same_environment_nonowner_lookup(self):
        chain=content.Chain.__new__(content.Chain)
        chain.roles={'peer':'readonly-owned-uuid-peer'}
        original={'templates':['CT-owned'], 'notes':[], 'audits':['template-save']}
        chain.ledger=mock.Mock(return_value=original)
        order=[]
        payload=content.template_payload('CV-owned', 'private template')
        def call(user, method, path, body, **kwargs):
            self.assertEqual((user,method,path,body),
                             ('peer','POST','/api/v1/canvas-templates',payload))
            order.append(('http',kwargs['want'],kwargs['code']))
            if kwargs['want']==403:
                self.assertEqual(order,[('http',403,'forbidden')])
            else:
                self.assertEqual(order[-2][0],'grant')
                self.assertEqual(kwargs['index'],1)
            return {}, {}
        def sql(statement):
            self.assertEqual(order,[('http',403,'forbidden')])
            self.assertIn("'readonly-owned-uuid-peer','canvas','change','environment','test'",statement)
            self.assertNotIn("'global'",statement)
            order.append(('grant',statement))
        chain.call=call; chain.sql=sql
        result=chain.template_peer_authorization(payload,original)
        self.assertEqual([row[0] for row in order],['http','grant','http'])
        self.assertEqual(chain.ledger.call_count,2)
        self.assertEqual(result['view_only_peer_status'],403)
        self.assertEqual(result['same_environment_change_nonowner_status'],404)

    def test_peer_content_write_stops_before_grant_or_next_request(self):
        chain=content.Chain.__new__(content.Chain)
        chain.roles={'peer':'owned-peer'}; chain.call=mock.Mock(); chain.sql=mock.Mock()
        chain.ledger=mock.Mock(return_value={'templates':['unexpected']})
        with self.assertRaisesRegex(AssertionError,'view-only peer rejection wrote content'):
            chain.template_peer_authorization({}, {'templates':[]})
        chain.sql.assert_not_called(); self.assertEqual(chain.call.call_count,1)

    def test_permitted_nonowner_rejection_must_also_preserve_ledger(self):
        chain=content.Chain.__new__(content.Chain)
        chain.roles={'peer':'owned-peer'}; chain.call=mock.Mock(); chain.sql=mock.Mock()
        chain.ledger=mock.Mock(side_effect=[{'templates':[]},{'templates':['unexpected']}])
        with self.assertRaisesRegex(AssertionError,'permitted nonowner rejection wrote content'):
            chain.template_peer_authorization({}, {'templates':[]})
        self.assertEqual(chain.call.call_count,2); chain.sql.assert_called_once()


if __name__ == '__main__': unittest.main()

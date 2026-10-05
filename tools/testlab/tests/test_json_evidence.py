"""Redaction must preserve source-bound structured evidence, not repair history."""
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from store import Store, scrub
from overview import _original_actual


class JsonEvidenceTests(unittest.TestCase):
    def authorization_event(self):
        return {'name': 'catalog.http', 'status': 'passed',
                'actual': {'status': 403},
                'interface': {'scenarios': {'authorization': {
                    'status': 'passed', 'actual_status': 403, 'expected_status': [401, 403],
                    'expected_detail': {'Authorization': 'Bearer offline-marker'},
                    'actual_detail': {'error_code': 'permission_denied'}}}}}

    def test_authorization_scenario_preserves_only_typed_outcomes(self):
        event = self.authorization_event()
        value = json.loads(scrub(event))
        result = value['interface']['scenarios']['authorization']
        self.assertEqual(result, {'status': 'passed', 'actual_status': 403,
                                 'expected_status': [401, 403],
                                 'expected_detail': '[REDACTED]', 'actual_detail': '[REDACTED]'})
        self.assertEqual(json.loads(scrub(value)), value)
        self.assertNotIn('offline-marker', json.dumps(value))
        three = {key: result[key] for key in ('status', 'actual_status', 'expected_status')}
        self.assertEqual(json.loads(scrub({'scenarios': {'authorization': three}}))[
            'scenarios']['authorization'], three)

    def test_authorization_exception_rejects_headers_wrong_paths_and_untyped_fields(self):
        event = self.authorization_event()
        outcome = event['interface']['scenarios']['authorization']
        for bad in (dict(outcome, token='offline-marker'), dict(outcome, actual_status=True),
                    dict(outcome, actual_status=600), dict(outcome, expected_status=[]),
                    dict(outcome, expected_status=['403']), dict(outcome, status='Bearer offline-marker')):
            self.assertEqual(json.loads(scrub({'scenarios': {'authorization': bad}}))[
                'scenarios']['authorization'], '[REDACTED]')
        for outer in ({'headers': {'authorization': outcome}},
                      {'actual': {'interface': event['interface']}},
                      {'Authorization': outcome}):
            self.assertNotIn('actual_status', scrub(outer))
            self.assertNotIn('offline-marker', scrub(outer))

    def test_persisted_authorization_result_and_secret_header_remain_separate(self):
        with tempfile.TemporaryDirectory() as temporary:
            ledger = Store(temporary)
            run = ledger.create_run()
            event = self.authorization_event()
            event['headers'] = {'Authorization': 'Bearer offline-marker', 'Cookie': 'offline-cookie'}
            ident = ledger.write_evidence(run['id'], scrub(event))
            raw, _ = ledger.read_evidence(ident)
            value = json.loads(raw)
            self.assertEqual(value['interface']['scenarios']['authorization']['actual_status'], 403)
            self.assertNotIn(b'offline-marker', raw)
            self.assertNotIn(b'offline-cookie', raw)

    def event(self):
        return {'name': 'ingress.private_source_configuration', 'status': 'passed',
                'actual': {'config_sha256': {'push-token': 'a'*64, 'source-token': 'c'*64,
                                           'operations.json': 'b'*64},
                           'main_runtime_modified': False}}

    def test_valid_json_redaction_preserves_assertions(self):
        value = json.loads(scrub(json.dumps(self.event())))
        hashes = value['actual']['config_sha256']
        self.assertEqual(hashes['push-token'], '[REDACTED]')
        self.assertEqual(hashes['source-token'], '[REDACTED]')
        self.assertEqual(hashes['operations.json'], 'b'*64)
        self.assertIs(value['actual']['main_runtime_modified'], False)

    def test_nested_sensitive_typed_values_are_removed(self):
        value = json.loads(scrub(json.dumps({'actual': [
            {'password': 42, 'token': {'nested': True}, 'ok': True}]})))
        self.assertEqual(value['actual'][0],
                         {'password': '[REDACTED]', 'token': '[REDACTED]', 'ok': True})

    def test_known_secret_with_json_escaping(self):
        marker = 'offline-marker"\n\\x'
        value = json.loads(scrub(json.dumps({'note': 'prefix '+marker+' suffix'}), [marker]))
        self.assertEqual(value['note'], 'prefix [REDACTED] suffix')

    def test_plaintext_safety_and_repeated_json_redaction(self):
        self.assertEqual(scrub('Bearer offline-marker'), 'Bearer [REDACTED]')
        once = scrub(json.dumps(self.event()))
        self.assertEqual(json.loads(scrub(once)), json.loads(once))

    def test_persisted_evidence_remains_readable_by_acceptance(self):
        with tempfile.TemporaryDirectory() as temporary:
            ledger = Store(temporary)
            run = ledger.create_run()
            ident = ledger.write_evidence(run['id'], scrub(json.dumps(self.event())))
            actual, evidence = _original_actual(ledger, {
                'name': self.event()['name'], 'status': 'passed', '_evidence_ids': [ident]})
            self.assertEqual(evidence, ident)
            self.assertIs(actual['main_runtime_modified'], False)
            self.assertEqual(actual['config_sha256']['push-token'], '[REDACTED]')
            raw, _ = ledger.read_evidence(ident)
            self.assertNotIn(('a'*64).encode(), raw)

    def test_malformed_historical_evidence_is_rejected(self):
        class Historical:
            def read_evidence(self, ident):
                return (b'{"name":"old","status":"passed","actual":{"token":[REDACTED]}}',
                        'text/plain')
        self.assertEqual(_original_actual(Historical(), {
            'name': 'old', 'status': 'passed', '_evidence_ids': ['old']}), ({}, None))


if __name__ == '__main__':
    unittest.main()

import json
import sys
import tempfile
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from store import Store


class EvidenceMimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.store = Store(Path(self.tmp.name))

    def test_content_in_mime_is_rejected_before_persistence(self):
        for mime in (json.dumps({'proof': True}), '{\n"proof": true\n}', 'text/plain\r\nX-Test: injected', None, 1):
            with self.subTest(mime=mime), self.assertRaises(ValueError):
                self.store.write_evidence('run', 'accidental label', mime=mime)
        self.assertEqual(list(self.store.evidence_dir.iterdir()), [])
        with self.store.connect() as connection:
            self.assertEqual(connection.execute('SELECT count(*) FROM evidence').fetchone()[0], 0)

    def test_json_content_and_type_are_kept_separate(self):
        ident = self.store.write_evidence('run', json.dumps({'proof': True}), mime='application/json; charset=utf-8')
        raw, mime = self.store.read_evidence(ident)
        self.assertEqual(json.loads(raw), {'proof': True})
        self.assertEqual(mime, 'application/json; charset=utf-8')

    def test_invalid_historical_mime_downloads_as_safe_text_without_rewrite(self):
        ident = self.store.write_evidence('run', 'original label')
        bad_mime = '{\n"proof": true\n}'
        with self.store.connect() as connection:
            connection.execute('UPDATE evidence SET mime=? WHERE id=?', (bad_mime, ident))
        raw, mime = self.store.read_evidence(ident)
        self.assertEqual(raw, b'original label')
        self.assertEqual(mime, 'text/plain; charset=utf-8')
        with self.store.connect() as connection:
            self.assertEqual(connection.execute('SELECT mime FROM evidence WHERE id=?', (ident,)).fetchone()[0], bad_mime)


if __name__ == '__main__':
    unittest.main()

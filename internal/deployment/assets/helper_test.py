"""Unprivileged archive/integrity checks; never invokes Docker, SSH or systemd."""
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('helper', Path(__file__).with_name('aiops-deploy-helper.py'))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class ArchiveSafety(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.config = {'root': self.temp.name, 'binary_name': 'aiops-demo',
                       'artifact_url_prefix': 'http://registered.invalid/bucket/'}

    def archive(self, name='aiops-demo', link=False, extra=False):
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode='w:gz') as tar:
            item = tarfile.TarInfo(name)
            if link:
                item.type = tarfile.SYMTYPE
                item.linkname = '/etc/shadow'
                tar.addfile(item)
            else:
                item.size = 6
                tar.addfile(item, io.BytesIO(b'binary'))
            if extra:
                tar.addfile(tarfile.TarInfo('extra'))
        return out.getvalue()

    def load(self, content, digest=None):
        request = {'artifact_url': 'http://registered.invalid/bucket/blob?signature=private',
                   'artifact_digest': digest or helper.digest(content)}
        with patch.object(helper.urllib.request, 'build_opener') as opener:
            opener.return_value.open.return_value = io.BytesIO(content)
            return helper.package(self.config, request)

    def test_valid_release_detects_tampering(self):
        blob = self.archive()
        release = self.load(blob)
        self.assertEqual((release / 'aiops-demo').read_bytes(), b'binary')
        self.assertEqual(self.load(blob), release)
        (release / 'aiops-demo').write_bytes(b'tampered')
        with self.assertRaises(ValueError):
            self.load(blob)

    def test_checksum_traversal_links_and_extra_entries_rejected(self):
        for blob in [self.archive('../aiops-demo'), self.archive(link=True), self.archive(extra=True)]:
            with self.assertRaises(ValueError):
                self.load(blob)
        with self.assertRaises(ValueError):
            self.load(self.archive(), 'sha256:' + '0' * 64)

    def test_extended_tar_metadata_rejected_before_extraction(self):
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode='w:gz', format=tarfile.PAX_FORMAT) as tar:
            item = tarfile.TarInfo('aiops-demo')
            item.pax_headers = {'comment': 'extension metadata'}
            item.size = 6
            tar.addfile(item, io.BytesIO(b'binary'))
        with self.assertRaises(ValueError):
            self.load(out.getvalue())

    def test_unregistered_origin_and_redirect_rejected(self):
        with self.assertRaises(ValueError):
            helper.package(self.config, {'artifact_url': 'http://other.invalid/bucket/blob'})
        with self.assertRaises(ValueError):
            helper.NoRedirect().redirect_request(None, None, None, None, None, None)

    def test_service_cannot_inherit_deployment_identity(self):
        config = dict(self.config, kind='systemd', unit='aiops-demo.service')
        for user in ('root', '0', '', 'aiops-deploy'):
            with patch.object(helper, 'command', return_value=user), patch.dict(helper.os.environ, {'SUDO_USER': 'aiops-deploy'}):
                with self.assertRaises(ValueError):
                    helper.snapshot(config)

    def test_observed_digest_and_activity_are_both_required(self):
        request = {'artifact_digest': 'sha256:' + 'a' * 64}
        config = {'kind': 'compose', 'image_repository': 'registry.invalid/app'}
        self.assertTrue(helper.matches(config, request, {'active': True, 'identity': 'registry.invalid/app@' + request['artifact_digest']}))
        self.assertFalse(helper.matches(config, request, {'active': False, 'identity': 'registry.invalid/app@' + request['artifact_digest']}))
        self.assertFalse(helper.matches(config, request, {'active': True, 'identity': 'registry.invalid/app:latest'}))

if __name__ == '__main__':
    unittest.main()

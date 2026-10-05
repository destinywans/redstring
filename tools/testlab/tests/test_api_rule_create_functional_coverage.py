import copy,datetime,json,ssl,stat,subprocess,sys,tempfile,unittest,uuid
from pathlib import Path
from types import SimpleNamespace
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rule_create_functional_coverage as create
class CreateTests(unittest.TestCase):
    def test_machine_contract_exact_operations(self):
        c=create.Contract(create.ROOT)
        self.assertEqual(c.canonical('POST','/api/v1/alert-rules'),('/api/v1/alert-rules','main'))
        self.assertEqual(c.canonical('GET','/api/v1/alert-rules/dry-runs/'+'a'*32)[0],'/api/v1/alert-rules/dry-runs/{id}')
        for m,p in [('POST','/api/v1/alert-rules/import'),('GET','/api/v1/alert-rules')]:
            with self.assertRaises(create.Blocked):c.canonical(m,p)
    def value(self):return dict(trial_id='a'*32,status='running',started_at='2026-10-01T00:00:00Z',expires_at='2026-10-02T00:00:00Z',last_update='2026-10-01T00:00:01Z',evaluated_points=1,missing_points=0,would_fire=1,windows=['x'])
    def test_exact_86400_window(self):
        create.validate_trial(self.value(),'a'*32)
        for patch in [dict(expires_at='2026-10-01T00:01:00Z'),dict(status='completed'),dict(evaluated_points=True),dict(trial_id='other'),dict(secret='bad')]:
            v=dict(self.value(),**patch)
            with self.assertRaises(AssertionError):create.validate_trial(v)
    def test_queued_nulls(self):
        v=dict(self.value(),status='queued',started_at=None,expires_at=None,evaluated_points=0);create.validate_trial(v)
        v['expires_at']='2026-10-02T00:00:00Z'
        with self.assertRaises(AssertionError):create.validate_trial(v)
    def test_spec_matches_shape_and_scoped_labels(self):
        v=create.spec('a'*32,'b'*32,'metric > 0');self.assertEqual(v['mode'],'dry_run');self.assertEqual(v['for'],'0s');self.assertEqual(v['labels']['application'],v['application_id']);self.assertEqual(v['environment_id'],v['labels']['environment'])
        with self.assertRaises(AssertionError):create.spec('wrong','b'*32,'metric > 0')
    def test_context_restores_all_shared_objects(self):
        before=(create.rules.PORTS,create.rules.Source,create.ro.emit,create.ro.transport,create.rules.hashes)
        with self.assertRaises(RuntimeError):
            with create.context():
                self.assertEqual(create.rules.PORTS,(18680,18681));raise RuntimeError('controlled')
        self.assertEqual(before,(create.rules.PORTS,create.rules.Source,create.ro.emit,create.ro.transport,create.rules.hashes))
    def trial_source(self,work):
        source=create.Source.__new__(create.Source);source.relay=mock.Mock();source.trial=None;source.extra=[]
        chain=SimpleNamespace(work=work,app='a'*32,config_hashes={})
        return source,chain
    def prepare_offline(self,source,chain):
        # No listeners or HTTP: retain real certificate loading, replacing socket wrapping only.
        with mock.patch.object(create.rules,'LocalServer') as server,mock.patch.object(ssl.SSLContext,'wrap_socket'):
            source.prepare_trial(chain)
        return server
    def test_expired_shared_certificate_is_not_reused(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder).resolve();shared=root/'.local/testlab/environments'/create.ro.OWNER;shared.mkdir(parents=True)
            cert,key=shared/'tls-cert.pem',shared/'tls-key.pem'
            subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','1','-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost','-out',str(cert),'-keyout',str(key)],check=True,capture_output=True,timeout=20)
            subprocess.run(['openssl','x509','-in',str(cert),'-signkey',str(key),'-days','0','-out',str(cert)],check=True,capture_output=True,timeout=20)
            expired=ssl._ssl._test_decode_cert(str(cert));self.assertLess(ssl.cert_time_to_seconds(expired['notAfter']),datetime.datetime.now(datetime.timezone.utc).timestamp())
            original=(cert.read_bytes(),key.read_bytes());work=root/str(uuid.uuid4());work.mkdir(mode=0o700)
            source,chain=self.trial_source(work)
            with mock.patch.object(create,'ROOT',root),mock.patch.object(create.ro,'private_json',return_value={'owner':create.ro.OWNER,'work':str(shared)}):
                self.prepare_offline(source,chain)
            actual=ssl._ssl._test_decode_cert(str(work/'tls-cert.pem'))
            self.assertGreater(ssl.cert_time_to_seconds(actual['notAfter']),datetime.datetime.now(datetime.timezone.utc).timestamp()+86400)
            self.assertIn(('DNS','localhost'),actual['subjectAltName'])
            subprocess.run(['openssl','verify','-CAfile',str(work/'tls-cert.pem'),'-verify_hostname','localhost',str(work/'tls-cert.pem')],check=True,capture_output=True,timeout=20)
            self.assertNotEqual((work/'tls-cert.pem').read_bytes(),original[0]);self.assertNotEqual((work/'tls-key.pem').read_bytes(),original[1])
            self.assertEqual((cert.read_bytes(),key.read_bytes()),original)
    def test_invocations_have_distinct_private_certificate_and_exact_trust(self):
        with tempfile.TemporaryDirectory() as folder,mock.patch.object(create.ro,'private_json',side_effect=AssertionError('shared TLS access')):
            identities=[]
            for _ in range(2):
                work=Path(folder)/str(uuid.uuid4());work.mkdir(mode=0o700);source,chain=self.trial_source(work)
                self.prepare_offline(source,chain)
                for name in ('tls-cert.pem','tls-key.pem'):
                    path=work/name;self.assertEqual(stat.S_IMODE(path.stat().st_mode),0o600);self.assertFalse(path.is_symlink())
                    self.assertEqual(chain.config_hashes[name],create.hashlib.sha256(path.read_bytes()).hexdigest())
                identities.append(((work/'tls-cert.pem').read_bytes(),(work/'tls-key.pem').read_bytes()))
                owner=create.Chain.__new__(create.Chain);owner.work=work;owner.rule_source_config=True
                with mock.patch.object(create.rules.Chain,'startup_environment',return_value={}):env=owner.startup_environment()
                self.assertEqual(env['SSL_CERT_FILE'],str(work/'tls-cert.pem'))
            self.assertNotEqual(identities[0][0],identities[1][0]);self.assertNotEqual(identities[0][1],identities[1][1])
    def test_certificate_generation_failure_closes_owned_sources(self):
        with tempfile.TemporaryDirectory() as folder:
            relay,exporter=mock.Mock(),mock.Mock();chain=SimpleNamespace(work=Path(folder),app='a'*32,config_hashes={})
            def base_init(source,chain):source.relay=relay;source.exporter=exporter
            with mock.patch.object(create.rules.Source,'__init__',base_init),mock.patch.object(create.rules,'LocalServer') as server,mock.patch.object(create.subprocess,'run',return_value=SimpleNamespace(returncode=1)),mock.patch.object(create.ro,'private_json',side_effect=AssertionError('shared TLS access')):
                with self.assertRaisesRegex(AssertionError,'TLS certificate generation failed'):create.Source(chain)
            relay.close.assert_called_once();exporter.close.assert_called_once();server.return_value.close.assert_called_once()
            self.assertEqual(server.call_count,1);self.assertEqual(chain.config_hashes,{})
if __name__=='__main__':unittest.main()

from __future__ import annotations
import base64,json,os,pathlib,signal,smtplib,socket,ssl,subprocess,sys,tempfile,threading,time,types,unittest
from unittest.mock import patch

TOOLS=pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0,str(TOOLS))
import invitations
import smtp_capture

class CaptureSession:
    def __init__(self,work,mode='accept'):
        self.work=pathlib.Path(work);(self.work/'mode.json').write_text(json.dumps({'mode':mode}))
        client,server=socket.socketpair();self.client=client
        config=types.SimpleNamespace(config={'username':'local-user','password':'synthetic-password'},work=self.work,capture_lock=threading.Lock())
        def run():
            try:smtp_capture.SMTPHandler(server,('127.0.0.1',1),config)
            finally:server.close()
        self.thread=threading.Thread(target=run,daemon=True);self.thread.start()
        self.smtp=smtplib.SMTP();self.smtp.sock=client;self.smtp.file=None
        status,_=self.smtp.getreply()
        if status!=220:raise AssertionError('SMTP banner missing')
    def close(self):
        self.smtp.close();self.thread.join(timeout=2)
        if self.thread.is_alive():raise AssertionError('capture connection did not stop')
    def captures(self):
        path=self.work/'captures.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

class InvitationsOfflineTests(unittest.TestCase):
    def setUp(self):
        self.directory=tempfile.TemporaryDirectory();self.addCleanup(self.directory.cleanup)
    def session(self,mode='accept'):
        value=CaptureSession(self.directory.name,mode);self.addCleanup(value.close);return value
    def authenticated(self,mode='accept'):
        session=self.session(mode);session.smtp.ehlo();session.smtp.login('local-user','synthetic-password');return session
    def test_generated_domain_is_only_destination(self):
        self.assertEqual(smtp_capture.local_address('<invite-ab@testlab.invalid>'),'invite-ab@testlab.invalid')
        for value in ['<user@gmail.com>','<user@sub.testlab.invalid>','user@testlab.invalid','<u@testlab.invalid>\r\nRCPT TO:<x@example.com>','<u@testlab.invalid>,<x@testlab.invalid>']:
            self.assertIsNone(smtp_capture.local_address(value))
    def test_authentication_required_and_secret_not_captured(self):
        session=self.session();session.smtp.ehlo()
        self.assertEqual(session.smtp.mail('noreply@testlab.invalid')[0],530)
        with self.assertRaises(smtplib.SMTPAuthenticationError):session.smtp.login('local-user','wrong')
        self.assertEqual(session.captures(),[])
    def test_external_recipient_refused_even_after_auth(self):
        session=self.authenticated();self.assertEqual(session.smtp.mail('noreply@testlab.invalid')[0],250)
        self.assertEqual(session.smtp.rcpt('external@example.com')[0],550)
        self.assertEqual(session.captures(),[])
    def test_external_sender_refused(self):
        session=self.authenticated();self.assertEqual(session.smtp.mail('external@example.com')[0],550)
        self.assertEqual(session.smtp.rcpt('user@testlab.invalid')[0],550)
    def test_advertised_size_cannot_exceed_capture_bound(self):
        session=self.authenticated()
        self.assertEqual(session.smtp.mail('noreply@testlab.invalid',options=['SIZE=131073'])[0],552)
        self.assertEqual(session.captures(),[])
    def test_accepted_data_is_durable_private_and_dot_unescaped(self):
        session=self.authenticated();session.smtp.sendmail('noreply@testlab.invalid','user@testlab.invalid','Subject: local\r\n\r\n.single dot\r\n')
        rows=session.captures();self.assertEqual(len(rows),1);self.assertEqual(rows[0]['outcome'],'accepted')
        self.assertIn('.single dot',rows[0]['message']);self.assertNotIn('synthetic-password',json.dumps(rows))
        self.assertEqual((session.work/'captures.jsonl').stat().st_mode&0o777,0o600)
    def test_data_rejection_has_no_success_ack(self):
        session=self.authenticated('reject_data')
        with self.assertRaises(smtplib.SMTPDataError) as rejected:session.smtp.sendmail('noreply@testlab.invalid','user@testlab.invalid','Subject: reject\r\n\r\nbody')
        self.assertEqual(rejected.exception.smtp_code,550);self.assertEqual(session.captures()[0]['outcome'],'rejected_data')
    def test_lost_data_ack_keeps_capture_without_success_response(self):
        session=self.authenticated('lose_data_response')
        with self.assertRaises(smtplib.SMTPServerDisconnected):session.smtp.sendmail('noreply@testlab.invalid','user@testlab.invalid','Subject: lost\r\n\r\nbody')
        self.assertEqual(session.captures()[0]['outcome'],'accepted_lost_response')
    def test_lost_quit_is_after_data_success(self):
        session=self.authenticated('lose_quit_response');session.smtp.sendmail('noreply@testlab.invalid','user@testlab.invalid','Subject: quit\r\n\r\nbody')
        with self.assertRaises(smtplib.SMTPServerDisconnected):session.smtp.quit()
        self.assertEqual(session.captures()[0]['outcome'],'accepted_quit_lost')
    def test_code_is_read_only_from_exact_single_private_capture(self):
        work=pathlib.Path(self.directory.name);code=base64.urlsafe_b64encode(bytes(range(32))).decode().rstrip('=')
        message=f'Trusted product entry: {invitations.ORIGIN}/accept-invitation\r\nOne-time invitation code: {code}\r\n'
        row={'recipient':'invite-ab@testlab.invalid','message':message,'outcome':'accepted'}
        (work/'captures.jsonl').write_text(json.dumps(row)+'\n');os.chmod(work/'captures.jsonl',0o600);config={'work':str(work)}
        self.assertEqual(invitations.captured(config,row['recipient']),(code,'accepted'))
        with self.assertRaises(AssertionError):invitations.captured(config,'other@testlab.invalid')
        (work/'captures.jsonl').write_text((json.dumps(row)+'\n')*2)
        with self.assertRaises(AssertionError):invitations.captured(config,row['recipient'])
    def test_snapshot_rejects_non_generated_addresses_before_sql(self):
        with patch.object(invitations,'sql') as query:
            for value in ['real@example.com',"invite-a@testlab.invalid'; DROP TABLE users;--",'../captures.jsonl']:
                with self.assertRaises(ValueError):invitations.snapshot({},value)
            query.assert_not_called()
    def test_https_only_api_client_has_fixed_origin(self):
        client=object.__new__(invitations.Client)
        with self.assertRaises(ValueError):client.request('GET','https://external.example/secret')
        self.assertEqual(invitations.ORIGIN,'https://testlab.local:18588')
    def test_cleanup_requires_owned_container_label(self):
        config=self.cleanup_config()
        record={'Name':'/'+config['container'],'Image':config['image_id'],'Config':{'Labels':{invitations.LABEL:'other-owner',invitations.RUN_LABEL:config['run_id']}}}
        with patch.object(invitations.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps([record]),stderr='')),patch.object(invitations,'command') as command:
            with self.assertRaises(RuntimeError):invitations.cleanup(config)
            command.assert_not_called()
    def cleanup_config(self):
        run='12345678-1234-4234-8234-123456789abc';owner='testlab-123456789abc'
        return {'owner':owner,'run_id':run,'work':str(invitations.ROOT/'.local/testlab/environments'/owner/'invitations'/run),
                'container':'aiops-'+owner+'-smtp-'+run[:8],'image_id':'sha256:synthetic'}
    def test_cleanup_checks_run_image_and_exact_name(self):
        config=self.cleanup_config()
        for field in ['run','image','name']:
            record={'Name':'/'+config['container'],'Image':config['image_id'],'Config':{'Labels':{invitations.LABEL:config['owner'],invitations.RUN_LABEL:config['run_id']}}}
            if field=='run':record['Config']['Labels'][invitations.RUN_LABEL]='other-run'
            if field=='image':record['Image']='sha256:other'
            if field=='name':record['Name']='/unrelated'
            with patch.object(invitations.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps([record]),stderr='')),patch.object(invitations,'command') as command:
                with self.assertRaises(RuntimeError):invitations.cleanup(config)
                command.assert_not_called()
    def test_partial_creation_cleanup_inspects_missing_and_unknown_resources(self):
        config=self.cleanup_config()
        with patch.object(invitations.subprocess,'run',return_value=types.SimpleNamespace(returncode=1,stdout='',stderr='Error: No such object')):
            invitations.cleanup(config)
        with patch.object(invitations.subprocess,'run',return_value=types.SimpleNamespace(returncode=1,stdout='',stderr='Docker network unavailable')):
            with self.assertRaises(RuntimeError):invitations.cleanup(config)
    def test_cleanup_rejects_arbitrary_resource_before_inspect(self):
        config=self.cleanup_config();config['container']='production-smtp'
        with patch.object(invitations.subprocess,'run') as inspect:
            with self.assertRaises(RuntimeError):invitations.cleanup(config)
            inspect.assert_not_called()
    def test_capture_read_rejects_symlink_and_public_permissions(self):
        work=pathlib.Path(self.directory.name);target=work/'message.jsonl';target.write_text('');target.chmod(0o644)
        (work/'captures.jsonl').symlink_to(target)
        with self.assertRaises(RuntimeError):invitations.captured({'work':str(work)},'invite-ab@testlab.invalid')
        (work/'captures.jsonl').unlink();target.rename(work/'captures.jsonl')
        with self.assertRaises(RuntimeError):invitations.captured({'work':str(work)},'invite-ab@testlab.invalid')
    def fixture_files(self):
        root=pathlib.Path(self.directory.name).resolve();owner='testlab-123456789abc';work=root/'.local/testlab/environments'/owner
        (work/'backend').mkdir(parents=True,exist_ok=True)
        instance=work/'instance.json';instance.write_text(json.dumps({'domain':'testlab.local'}))
        dsn='postgres://testlab:synthetic@127.0.0.1:15461/aiops_testlab_app'
        fixture={'owner':owner,'postgres_container':'aiops-'+owner+'-pg','database_url':dsn,'work':str(work/'backend'),
                 'instance_config':str(instance),'binary':str(work/'backend/aiops'),'source_tree_sha':'synthetic-sha'}
        environment={'owner':owner,'container':fixture['postgres_container'],'work':str(work),'env':{'TESTLAB_APP_DSN':dsn}}
        for filename,data in [('backend-fixture.json',fixture),('environment.json',environment)]:
            path=root/'.local/testlab'/filename;path.write_text(json.dumps(data));path.chmod(0o600)
        return root,fixture,environment
    def test_owned_fixture_matches_all_owned_boundaries(self):
        root,fixture,_=self.fixture_files()
        with patch.object(invitations,'ROOT',root),patch.object(invitations,'command',return_value=json.dumps({invitations.LABEL:fixture['owner']})),patch.object(invitations,'source_fingerprint',return_value='synthetic-sha'):
            self.assertEqual(invitations.owned_fixture()[0]['owner'],fixture['owner'])
    def test_owned_fixture_refuses_wrong_database_port_user_and_workspace(self):
        for field,value in [('database_url','postgres://testlab:synthetic@127.0.0.1:5432/aiops_testlab_app'),
                            ('database_url','postgres://production:synthetic@127.0.0.1:15461/aiops_testlab_app'),
                            ('instance_config','/tmp/unrelated-instance.json'),('binary','/tmp/unrelated-binary')]:
            root,fixture,_=self.fixture_files();fixture[field]=value
            path=root/'.local/testlab/backend-fixture.json';path.write_text(json.dumps(fixture))
            with patch.object(invitations,'ROOT',root),patch.object(invitations,'command') as command:
                with self.assertRaises(RuntimeError):invitations.owned_fixture()
                command.assert_not_called()
    def test_owned_fixture_refuses_environment_symlink_or_public_permissions(self):
        root,_,_=self.fixture_files();path=root/'.local/testlab/environment.json';path.chmod(0o644)
        with patch.object(invitations,'ROOT',root):
            with self.assertRaises(RuntimeError):invitations.owned_fixture()
        path.chmod(0o600);target=path.with_name('environment-other.json');path.rename(target);path.symlink_to(target)
        with patch.object(invitations,'ROOT',root):
            with self.assertRaises(RuntimeError):invitations.owned_fixture()
    def test_tls_smtp_transport_validates_ca_and_exact_authentication(self):
        work=pathlib.Path(self.directory.name)
        subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(work/'smtp.key'),
                        '-out',str(work/'smtp.crt'),'-days','1','-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,IP:127.0.0.1'],
                       check=True,capture_output=True,timeout=10)
        (work/'smtp-config.json').write_text(json.dumps({'username':'local-user','password':'synthetic-password'}))
        (work/'mode.json').write_text(json.dumps({'mode':'accept'}))
        server=smtp_capture.SMTPServer(work,0,'127.0.0.1');thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with self.assertRaises(ssl.SSLCertVerificationError):
                with smtplib.SMTP_SSL('127.0.0.1',server.server_address[1],context=ssl.create_default_context(),timeout=3):pass
            trusted=ssl.create_default_context(cafile=str(work/'smtp.crt'))
            with smtplib.SMTP_SSL('127.0.0.1',server.server_address[1],context=trusted,timeout=3) as smtp:
                with self.assertRaises(smtplib.SMTPAuthenticationError):smtp.login('local-user','incorrect')
                smtp.login('local-user','synthetic-password')
                smtp.sendmail('noreply@testlab.invalid','user@testlab.invalid','Subject: TLS\r\n\r\nlocal')
            rows=[json.loads(line) for line in (work/'captures.jsonl').read_text().splitlines()]
            self.assertEqual(len(rows),1);self.assertEqual(rows[0]['outcome'],'accepted')
        finally:server.shutdown();server.server_close();thread.join(timeout=2)
    def test_sigterm_unwinds_owned_chain_and_records_interruption(self):
        work=pathlib.Path(self.directory.name)
        script="""
import pathlib,signal,sys,time
sys.path.insert(0,sys.argv[1])
import invitations
work=pathlib.Path(sys.argv[2])
with invitations.cooperative_sigterm():
 invitations._active_work=work
 print('ready',flush=True)
 try:
  while True:time.sleep(.1)
 except invitations.TestlabInterrupted:
  invitations._private(work/'caught.json',{'signal':invitations._interrupt_signal})
 finally:
  signal.raise_signal(signal.SIGTERM)
  invitations._private(work/'cleanup-confirmed.json',{'fake_owned_resource_stopped':True})
sys.exit(143)
"""
        proc=subprocess.Popen([sys.executable,'-c',script,str(TOOLS),str(work)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        try:
            self.assertEqual(proc.stdout.readline().strip(),'ready');began=time.monotonic()
            proc.send_signal(signal.SIGTERM);out,err=proc.communicate(timeout=2)
            self.assertLess(time.monotonic()-began,2);self.assertEqual(proc.returncode,143,err)
            self.assertEqual(json.loads((work/'caught.json').read_text())['signal'],signal.SIGTERM)
            exit_record=json.loads((work/'interrupted-exit.json').read_text())
            self.assertTrue(exit_record['cleanup_confirmed']);self.assertEqual(exit_record['pid'],proc.pid)
            self.assertEqual((work/'interrupted-exit.json').stat().st_mode&0o777,0o600)
        finally:
            if proc.poll() is None:proc.kill();proc.wait(timeout=2)
            proc.stdout.close();proc.stderr.close()
    def test_interrupted_cleanup_keeps_identity_guards_and_bounded_timeout(self):
        config=self.cleanup_config()
        record={'Name':'/'+config['container'],'Image':config['image_id'],'Config':{'Labels':{invitations.LABEL:config['owner'],invitations.RUN_LABEL:config['run_id']}}}
        with patch.object(invitations,'_interrupt_signal',signal.SIGTERM),patch.object(invitations.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps([record]),stderr='')) as inspect,patch.object(invitations,'command') as command,patch.object(invitations,'_private'):
            invitations.cleanup(config)
            self.assertEqual(inspect.call_args.kwargs['timeout'],5)
            self.assertEqual(command.call_args.args[0],['docker','stop','--time','5',config['container']])
            self.assertEqual(command.call_args.kwargs['timeout'],10)
    def test_sigterm_handler_is_restored_and_unconfirmed_cleanup_not_success(self):
        previous=signal.getsignal(signal.SIGTERM);work=pathlib.Path(self.directory.name)
        with invitations.cooperative_sigterm():
            invitations._active_work=work
            with self.assertRaises(invitations.TestlabInterrupted):signal.raise_signal(signal.SIGTERM)
        self.assertEqual(signal.getsignal(signal.SIGTERM),previous)
        self.assertFalse(json.loads((work/'interrupted-exit.json').read_text())['cleanup_confirmed'])
    def test_first_sigterm_during_finally_does_not_abort_cleanup(self):
        work=pathlib.Path(self.directory.name)
        with patch.object(sys,'argv',['invitations','chain']):
            def owned_chain():
                invitations._active_work=work
                with invitations.interruption_cleanup():
                    signal.raise_signal(signal.SIGTERM)
                    invitations._private(work/'cleanup-confirmed.json',{'owned_container_stopped':True})
                return 0
            with patch.object(invitations,'chain',side_effect=owned_chain):self.assertEqual(invitations.main(),143)
        self.assertTrue(json.loads((work/'interrupted-exit.json').read_text())['cleanup_confirmed'])

if __name__=='__main__':unittest.main()

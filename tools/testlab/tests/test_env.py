"""Ownership/failure tests mock all Docker, process and socket operations.

They never stop the running testlab or delete its containers/volumes.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import env


class EnvironmentTests(unittest.TestCase):
    def setUp(self):
        ports=patch.object(env,'_port_open',return_value=False)
        ports.start();self.addCleanup(ports.stop)
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name).resolve();self.state=self.root/'.local/testlab/environment.json'
        self.owner='testlab-unittest';self.work=self.root/'.local/testlab/environments'/self.owner
        self.data={'owner':self.owner,'container':'aiops-'+self.owner+'-pg','work':str(self.work),'run_id':'run-one','status':'ready','env':{}}
        for key,database in {'TEST_DATABASE_URL':'aiops_testlab','TESTLAB_MFA_DSN':'wan124mfa','TESTLAB_PASSKEY_DSN':'wan124passkey','TESTLAB_FORMAL_DSN':'wan124test','TESTLAB_APP_DSN':'aiops_testlab_app'}.items():
            self.data['env'][key]=f'postgres://testlab:synthetic@127.0.0.1:15461/{database}'

    def write(self):
        env._private(self.state,self.data)
        env._private(self.work/'instance.json',{'tier':'nonprod','environments':[{'id':'test'},{'id':'staging'}]})

    def pg(self,owner=None):
        return {'Config':{'Labels':{env.LABEL:owner or self.owner}},'State':{'Running':True},'HostConfig':{'Memory':2<<30,'NanoCpus':2_000_000_000},'NetworkSettings':{'Ports':{'5432/tcp':[{'HostIp':'127.0.0.1','HostPort':'15461'}]}}}

    def inspection(self,name,kind=None):
        return self.pg() if name==self.data['container'] else None

    def test_null_run_binding_is_persisted_once(self):
        self.data['run_id']=None;self.write()
        with patch.object(env,'_inspect',side_effect=self.inspection):
            self.assertEqual(env.prepare(self.root,'first')['run_id'],'first')
            self.assertEqual(env.prepare(self.root,'later')['run_id'],'first')
        self.assertEqual(json.loads(self.state.read_text())['run_id'],'first')

    def test_foreign_pg_refuses_prepare_without_binding(self):
        self.data['run_id']=None;self.write()
        with patch.object(env,'_inspect',return_value=self.pg('testlab-foreign')):
            with self.assertRaises(ValueError):env.prepare(self.root,'first')
        self.assertIsNone(json.loads(self.state.read_text())['run_id'])

    def test_run_mismatch_refuses_all_cleanup_side_effects(self):
        self.write()
        with patch.object(env,'_inspect') as inspect,patch.object(env,'_stop_backend') as stop,patch.object(env,'_cmd') as command:
            with self.assertRaises(ValueError):env.cleanup(self.root,'unrelated')
            inspect.assert_not_called();stop.assert_not_called();command.assert_not_called()
        self.assertTrue(self.state.exists())

    def test_foreign_backend_trial_network_refuse_before_any_cleanup(self):
        for target in ('backend','trial','network'):
            with self.subTest(target=target):
                self.write();backend=self.root/'.local/testlab/backend-fixture.json'
                env._private(backend,{'owner':'testlab-foreign' if target=='backend' else self.owner})
                def inspect(name,kind=None):
                    if target=='trial' and name.endswith('trial-prometheus'):return self.pg('testlab-foreign')
                    if target=='network' and kind=='network':return {'Labels':{env.LABEL:'testlab-foreign'}}
                    return self.inspection(name,kind)
                with patch.object(env,'_inspect',side_effect=inspect),patch.object(env,'_stop_backend') as stop,patch.object(env,'_cleanup_components') as components,patch.object(env,'_cmd') as command:
                    with self.assertRaises(ValueError):env.cleanup(self.root,'run-one')
                    stop.assert_not_called();components.assert_not_called();command.assert_not_called()
                self.assertTrue(self.state.exists())

    def test_cleanup_order_owned_trial_and_pg_evidence_retained(self):
        self.write();self.work.mkdir(parents=True,exist_ok=True)
        evidence=self.work/'evidence.json';evidence.write_text('synthetic retained evidence')
        env._private(self.root/'.local/testlab/backend-fixture.json',{'owner':self.owner})
        env._private(self.root/'.local/testlab/runtime-config.json',{'owner':self.owner})
        events=[]
        def inspect(name,kind=None):return self.pg() if name.endswith('-pg') or name.endswith('trial-prometheus') else None
        with patch.object(env,'_inspect',side_effect=inspect),patch.object(env,'_stop_backend',side_effect=lambda _:events.append('backend')),patch.object(env,'_cleanup_components',side_effect=lambda _:events.append('components')),patch.object(env,'_cmd',side_effect=lambda argv,**_:events.append(argv[-1])):
            result=env.cleanup(self.root,'run-one')
        self.assertEqual(events,['backend','components','aiops-'+self.owner+'-trial-prometheus',self.data['container']])
        self.assertEqual(result['status'],'cleaned');self.assertEqual(evidence.read_text(),'synthetic retained evidence');self.assertFalse(self.state.exists())

    def test_active_run_refuses_cleanup_before_docker(self):
        self.write()
        from store import Store
        store=Store(self.root);run=store.create_run('synthetic','test');store.update('runs',run['id'],status='running',pid=os.getpid())
        with patch.object(env,'_inspect') as inspect,patch.object(env,'_stop_backend') as stop,patch.object(env,'_cmd') as command:
            with self.assertRaisesRegex(RuntimeError,'active'):env.cleanup(self.root,'run-one')
            inspect.assert_not_called();stop.assert_not_called();command.assert_not_called()

    def test_foreign_heavy_resource_refused_before_any_stop(self):
        self.write()
        env._private(self.root/'.local/testlab/gitops-environment.json',{'owner':self.owner,'work':str(self.work/'gitops'),'gitlab_name':'aiops-'+self.owner+'-gitlab','cluster':'aiops-backend-testlab','kubeconfig':str(self.work/'gitops/kubeconfig')})
        def inspect(name,kind=None):return self.pg('testlab-foreign') if name.endswith('-gitlab') else self.inspection(name,kind)
        with patch.object(env,'_inspect',side_effect=inspect),patch.object(env,'_stop_backend') as stop,patch.object(env,'_cleanup_components') as components,patch.object(env,'_cmd') as command:
            with self.assertRaises(ValueError):env.cleanup(self.root,'run-one')
            stop.assert_not_called();components.assert_not_called();command.assert_not_called()
        self.assertTrue(self.state.exists())

    def test_temporary_https_entry_refuses_cleanup(self):
        self.write()
        with patch.object(env,'_port_open',side_effect=lambda p:p==18583),patch.object(env,'_inspect') as inspect:
            with self.assertRaisesRegex(RuntimeError,'HTTPS'):env.cleanup(self.root,'run-one')
            inspect.assert_not_called()

    def test_temporary_consumer_ports_refuse_cleanup_before_any_docker_mutation(self):
        self.write()
        for port in (465, 18586, 18587, 18588, 18589, 18590, 18591, 18592, 18594, 18595, 18596, 18597, 18598, 18599, 18600):
            with self.subTest(port=port), patch.object(env, '_port_open', side_effect=lambda p: p == port), patch.object(env, '_inspect') as inspect:
                with self.assertRaisesRegex(RuntimeError, 'consumer entry'):
                    env.cleanup(self.root, 'run-one')
                inspect.assert_not_called()

    def test_owned_heavy_cleanup_retains_only_its_records_and_volumes(self):
        self.write();heavy={'owner':self.owner,'work':str(self.work/'gitops'),'gitlab_name':'aiops-'+self.owner+'-gitlab','cluster':'aiops-backend-testlab','kubeconfig':str(self.work/'gitops/kubeconfig')}
        env._private(self.root/'.local/testlab/gitops-environment.json',heavy)
        def inspect(name,kind=None):
            if name==heavy['gitlab_name'] or name==heavy['cluster']+'-control-plane':
                info=self.pg();info['Mounts']=[{'Type':'volume','Name':'owned-retained-'+name}];return info
            return self.inspection(name,kind)
        with patch.object(env,'_inspect',side_effect=inspect),patch.object(env,'_cleanup_components'),patch.object(env,'_cmd') as command:
            result=env.cleanup(self.root,'run-one')
        self.assertEqual([c.args[0][-1] for c in command.call_args_list],[heavy['gitlab_name'],heavy['cluster']+'-control-plane',self.data['container']])
        self.assertEqual(len(result['retained_docker_volumes']),2)
        self.assertEqual(json.loads((self.work/'gitops/retained-environment.json').read_text()),heavy)
        self.assertFalse((self.root/'.local/testlab/gitops-environment.json').exists())

    def test_creation_failure_persists_owner_before_resource_and_can_reclaim(self):
        captured={}
        class ClosedSocket:
            def __enter__(self):return self
            def __exit__(self,*_):pass
            def connect_ex(self,_):return 1
        def fail(argv,**_):
            current=json.loads(self.state.read_text())
            self.assertEqual(current['status'],'creating');self.assertEqual(current['run_id'],'failed-run')
            captured.update(current);raise RuntimeError('synthetic Docker create failed')
        with patch.object(env.socket,'socket',return_value=ClosedSocket()),patch.object(env,'_cmd',side_effect=fail):
            with self.assertRaises(RuntimeError):env.prepare(self.root,'failed-run')
        record=json.loads(self.state.read_text());self.assertEqual(record['status'],'failed');self.assertEqual(record['owner'],captured['owner'])
        retained=Path(record['work'])/'postgres.env';self.assertTrue(retained.exists())
        with patch.object(env,'_inspect',return_value=None),patch.object(env,'_cmd') as command,patch.object(env,'_cleanup_components') as components:
            self.assertEqual(env.cleanup(self.root,'failed-run')['status'],'cleaned');command.assert_not_called();components.assert_not_called()
        self.assertTrue(retained.exists())

    def test_failure_after_creation_retains_and_reclaims_exact_label(self):
        class ClosedSocket:
            def __enter__(self):return self
            def __exit__(self,*_):pass
            def connect_ex(self,_):return 1
        with patch.object(env.socket,'socket',return_value=ClosedSocket()),patch.object(env,'_cmd',return_value='created'),patch.object(env.subprocess,'run',return_value=subprocess.CompletedProcess([],1)),patch.object(env.time,'sleep'):
            with self.assertRaises(RuntimeError):env.prepare(self.root,'failed-after-create')
        data=json.loads(self.state.read_text());self.assertEqual(data['status'],'failed')
        def inspect(name,kind=None):return {'Config':{'Labels':{env.LABEL:data['owner']}}} if name==data['container'] else None
        with patch.object(env,'_inspect',side_effect=inspect),patch.object(env,'_cleanup_components'),patch.object(env,'_cmd') as command:
            env.cleanup(self.root,'failed-after-create');command.assert_called_once_with(['docker','rm','-f',data['container']])
        self.assertTrue(Path(data['work']).exists())

    def test_missing_pg_still_delegates_authenticated_component_cleanup(self):
        self.write();env._private(self.root/'.local/testlab/component-environment.json',{'owner':self.owner})
        with patch.object(env,'_inspect',return_value=None),patch.object(env,'_cleanup_components') as components,patch.object(env,'_cmd') as command:
            self.assertEqual(env.cleanup(self.root,'run-one')['status'],'cleaned')
            components.assert_called_once_with(self.root);command.assert_not_called()

    def test_atomic_private_failure_keeps_previous_state_and_no_temp(self):
        self.write();before=self.state.read_bytes()
        with patch.object(env.os,'replace',side_effect=OSError('synthetic disk error')):
            with self.assertRaises(OSError):env._private(self.state,{'replacement':True})
        self.assertEqual(self.state.read_bytes(),before);self.assertEqual(self.state.stat().st_mode&0o777,0o600)
        self.assertEqual(list(self.state.parent.glob('.environment.json-*')),[])

    def test_private_symlink_refused(self):
        self.state.parent.mkdir(parents=True);other=self.root/'other.json';other.write_text('untouched');self.state.symlink_to(other)
        with self.assertRaises(ValueError):env._private(self.state,{'replacement':True})
        self.assertEqual(other.read_text(),'untouched')

    def test_public_snapshot_contains_no_database_or_account_credentials(self):
        self.write()
        env._private(self.root/'.local/testlab/backend-fixture.json',{'owner':self.owner,'admin':{'password':'must-never-export'},'database_url':'postgres://testlab:must-never-export@127.0.0.1:15461/aiops_testlab_app','seed':20260930,'base_urls':['http://127.0.0.1:18561']})
        with patch.object(env,'_inspect',side_effect=self.inspection):snapshot=env.public_snapshot(self.root)
        text=json.dumps(snapshot)
        self.assertNotIn('must-never-export',text);self.assertNotIn('postgres://',text)
        self.assertEqual(snapshot['postgres']['databases'],['aiops_testlab','wan124mfa','wan124passkey','wan124test','aiops_testlab_app'])

    def test_public_snapshot_refuses_wrong_database_or_foreign_label(self):
        self.write()
        with patch.object(env,'_inspect',return_value=self.pg('testlab-foreign')):
            with self.assertRaises(ValueError):env.public_snapshot(self.root)
        self.data['env']['TEST_DATABASE_URL']='postgres://testlab:synthetic@127.0.0.1:5432/business';self.write()
        with patch.object(env,'_inspect',side_effect=self.inspection):
            with self.assertRaisesRegex(ValueError,'contract'):env.public_snapshot(self.root)

    def doctor(self,occupied=(),extensions='pgcrypto\nvector',inspection=None):
        def command(argv,**_):return json.dumps({'NCPU':8,'MemTotal':8<<30}) if argv[1]=='info' else extensions
        with patch.object(env,'_cmd',side_effect=command),patch.object(env,'_inspect',side_effect=inspection or self.inspection),patch.object(env,'_process_ports',return_value={}),patch.object(env,'_port_open',side_effect=lambda port:port in occupied),patch.object(env.shutil,'which',return_value='/synthetic/tool'):
            return {r['name']:r for r in env.doctor(self.root)}

    def test_doctor_never_claims_isolation_without_owned_state(self):
        rows=self.doctor();self.assertEqual(rows['isolation']['status'],'blocked')

    def test_doctor_owned_vs_foreign_occupied_port_and_extensions(self):
        self.write()
        self.assertEqual(self.doctor([15461])['isolation']['status'],'passed')
        rows=self.doctor([15461,18561]);self.assertEqual(rows['port:18561']['status'],'blocked');self.assertEqual(rows['isolation']['status'],'blocked')
        self.assertEqual(self.doctor([15461],extensions='vector')['isolation']['status'],'blocked')

    def test_doctor_stopped_container_cannot_prove_occupied_port(self):
        self.write()
        def inspect(name,kind=None):
            if name.endswith('-prometheus'):
                info=self.pg();info['State']['Running']=False;info['NetworkSettings']['Ports']={'9090/tcp':[{'HostIp':'127.0.0.1','HostPort':'18570'}]};return info
            return self.inspection(name,kind)
        self.assertEqual(self.doctor([15461,18570],inspection=inspect)['isolation']['status'],'blocked')

    def test_hmac_fixture_pid_covers_its_actual_prometheus_tls_listener(self):
        self.write();env._private(self.work/'component-environment.json',{'owner':self.owner,'fixture_pid':12345})
        import components
        with patch.object(components,'fixture_alive',return_value=True),patch.object(env,'_process_port',side_effect=lambda pid,port:pid==12345 and port in (18569,18577,18578,18579)):
            self.assertIn(18569,env._process_ports(self.root,self.owner,self.work))


if __name__=='__main__':unittest.main()

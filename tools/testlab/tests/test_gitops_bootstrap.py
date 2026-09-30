import importlib.util, io, json, sys, tempfile, unittest, urllib.error
from pathlib import Path
from unittest.mock import Mock, patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import gitops


class GitOpsBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name).resolve();self.owner='testlab-abcdef123456'
        self.work=self.root/'.local/testlab/environments'/self.owner/'gitops';self.work.mkdir(parents=True)
        self.data={'owner':self.owner,'work':str(self.work),'cluster':gitops.CLUSTER,'gitlab_name':'aiops-'+self.owner+'-gitlab','argo_url':'https://localhost:18581','runtime_project_id':1,'runtime_project_initialized':True,'runtime_repo_url':'https://host.docker.internal:18580/root/testlab-rule-runtime.git'}
        (self.work/'argo-install.yaml').write_text('official manifest fixture')
        (self.work/'tls.crt').write_text('owned certificate fixture');(self.work/'tls.crt').chmod(0o600)
        (self.work/'tls.key').write_text('owned private fixture');(self.work/'tls.key').chmod(0o600)
        self.pods={'items':[{'status':{'phase':'Running','containerStatuses':[{'ready':True}]}} for _ in range(5)]}
        self.namespace={'metadata':{'labels':{'aiops.testlab.owner':self.owner}}}
        self.gitlab={'State':{'Running':True},'NetworkSettings':{'Ports':{'443/tcp':[{'HostIp':'127.0.0.1','HostPort':'18580'}]}}}

    def test_official_install_restores_git_ca_before_any_readiness_or_credentials(self):
        calls=[]
        def kube(data,*args,**kwargs):
            calls.append((args,kwargs))
            if args[:2]==('get','namespace'):return json.dumps(self.namespace)
            if args[:2]==('get','pods'):return json.dumps(self.pods)
            if args[:2]==('get','configmap'):return json.dumps({'data':{'host.docker.internal':(self.work/'tls.crt').read_text()}})
            return ''
        with patch.object(gitops,'ROOT',self.root),patch.object(gitops,'kubectl',side_effect=kube),patch.object(gitops,'owned_container',return_value=self.gitlab),patch.object(gitops,'save'),patch.object(gitops,'emit'):
            gitops.argo(self.data)
        install=next(i for i,(args,_) in enumerate(calls) if args==('apply','-n','argocd','-f',str(self.work/'argo-install.yaml')))
        restores=[i for i,(_,kw) in enumerate(calls) if kw.get('input') and json.loads(kw['input']).get('kind')=='ConfigMap' and json.loads(kw['input']).get('data',{}).get('host.docker.internal')=='owned certificate fixture']
        self.assertTrue(restores,'official apply must restore the owned GitLab trust key')
        self.assertGreater(restores[0],install)
        first_pods=next(i for i,(args,_) in enumerate(calls) if args[:2]==('get','pods'))
        self.assertLess(restores[0],first_pods)

    def test_runtime_transport_readiness_precedes_session_credential_post(self):
        events=[]
        def kube(data,*args,**kwargs):
            if args[:2]==('get','secret'):return json.dumps({'data':{'password':'c3ludGhldGlj'}})
            if args[:2]==('get','namespace'):return json.dumps(self.namespace)
            return ''
        def argo(data,method,path,*args,**kwargs):
            if method=='POST' and path=='/api/v1/session':events.append('credential_post');raise RuntimeError('stop before environment mutation')
            return {}
        response=Mock();response.status=200;response.read.return_value=b'ok';response.__enter__=Mock(return_value=response);response.__exit__=Mock(return_value=False)
        def transport(*args,**kwargs):events.append('verified_readiness');return response
        self.data['ca_file']=str(self.work/'tls.crt')
        with patch.object(gitops,'cert',return_value=(self.work/'tls.crt',self.work/'tls.key')),patch.object(gitops,'kubectl',side_effect=kube),patch.object(gitops,'argo_http',side_effect=argo),patch.object(gitops.urllib.request,'urlopen',side_effect=transport),patch.object(gitops.ssl,'create_default_context'),self.assertRaisesRegex(RuntimeError,'stop before environment mutation'):
            gitops.runtime_application(self.data)
        self.assertEqual(events,['verified_readiness','credential_post'])

    def test_failed_prepare_invalidates_old_success_before_chain_can_run(self):
        self.data['bootstrap_ready']=True
        with patch.object(sys,'argv',['gitops.py','prepare']),patch.object(gitops,'setup_state',return_value=self.data),patch.object(gitops,'save'),patch.object(gitops,'gitlab'),patch.object(gitops,'review_probe',return_value=True),patch.object(gitops,'kind',side_effect=RuntimeError('TLS EOF')),patch.object(gitops,'emit'):
            self.assertEqual(gitops.main(),2)
        self.assertIs(self.data.get('bootstrap_ready'),False)

    def test_chain_refuses_half_prepared_state_before_loading_business_runner(self):
        self.data['bootstrap_ready']=False
        fake=Mock()
        with patch.object(sys,'argv',['gitops.py','chain']),patch.object(gitops,'setup_state',return_value=self.data),patch.object(gitops,'emit'),patch.object(importlib.util,'spec_from_file_location',return_value=Mock()),patch.object(importlib.util,'module_from_spec',return_value=fake):
            self.assertEqual(gitops.main(),2)
        fake.run.assert_not_called()

    def test_foreign_namespace_refuses_official_apply_and_ca_restore(self):
        with patch.object(gitops,'kubectl',return_value=json.dumps({'metadata':{'labels':{'aiops.testlab.owner':'foreign-owner'}}})) as kube,patch.object(gitops,'save'),patch.object(gitops,'emit'),self.assertRaises(gitops.BootstrapBlocked):
            gitops.argo(self.data)
        self.assertEqual(kube.call_count,1);self.assertEqual(kube.call_args.args[1:3],('get','namespace'))

    def test_foreign_gitlab_listener_refuses_ca_write(self):
        wrong={'State':{'Running':True},'NetworkSettings':{'Ports':{'443/tcp':[{'HostIp':'0.0.0.0','HostPort':'18580'}]}}}
        with patch.object(gitops,'ROOT',self.root),patch.object(gitops,'kubectl',return_value=json.dumps(self.namespace)) as kube,patch.object(gitops,'owned_container',return_value=wrong),self.assertRaises(gitops.BootstrapBlocked):gitops.restore_gitlab_ca(self.data)
        self.assertEqual(kube.call_count,1)

    def test_readiness_retries_only_verified_get_and_has_bounded_deadline(self):
        self.data['ca_file']=str(self.work/'tls.crt');context=Mock()
        response=Mock();response.status=200;response.read.return_value=b'ok';response.__enter__=Mock(return_value=response);response.__exit__=Mock(return_value=False)
        with patch.object(gitops.ssl,'create_default_context',return_value=context) as ssl_context,patch.object(gitops.urllib.request,'urlopen',side_effect=[urllib.error.URLError('TLS EOF'),response]) as transport,patch.object(gitops.time,'sleep'):
            gitops.wait_argo_api(self.data)
        ssl_context.assert_called_once_with(cafile=self.data['ca_file'])
        for call in transport.call_args_list:
            self.assertIs(call.kwargs['context'],context);self.assertEqual(call.args[0].get_method(),'GET');self.assertEqual(call.args[0].full_url,'https://localhost:18581/healthz');self.assertLessEqual(call.kwargs['timeout'],3)
        with patch.object(gitops.ssl,'create_default_context',return_value=context),patch.object(gitops.urllib.request,'urlopen',side_effect=urllib.error.URLError('TLS EOF')) as transport,patch.object(gitops.time,'monotonic',side_effect=[0,0,.1,.2,1.1]),patch.object(gitops.time,'sleep'),self.assertRaises(gitops.BootstrapBlocked):gitops.wait_argo_api(self.data,seconds=1)
        self.assertEqual(transport.call_count,1)

    def test_http400_retains_private_body_and_exact_path_without_request_secrets(self):
        self.data['ca_file']=str(self.work/'tls.crt')
        body=json.dumps({'code':9,'message':'synthetic unknown-authority diagnostic'}).encode()
        failure=urllib.error.HTTPError('https://localhost:18581/api/v1/applications/testlab-rules/sync',400,'Bad Request',{},io.BytesIO(body))
        with patch.object(gitops.ssl,'create_default_context'),patch.object(gitops.urllib.request,'urlopen',side_effect=failure),self.assertRaises(RuntimeError) as error:gitops.argo_http(self.data,'POST','/api/v1/applications/testlab-rules/sync',{'secret':'request-canary'},token='credential-canary')
        p=next(self.work.glob('argo-http-red-*.json'));self.assertEqual(p.stat().st_mode&0o777,0o600);raw=p.read_text();record=json.loads(raw)
        self.assertEqual(record['status'],400);self.assertEqual(json.loads(record['body'])['code'],9);self.assertEqual(record['url'],'https://localhost:18581/api/v1/applications/testlab-rules/sync')
        self.assertNotIn('credential-canary',raw);self.assertNotIn('request-canary',raw);self.assertNotIn('unknown-authority',str(error.exception))

    def test_successful_marker_still_rechecks_exact_authoritative_application(self):
        self.data['bootstrap_ready']=True;self.data['argo_token_file']=str(self.work/'argo-read-token')
        Path(self.data['argo_token_file']).write_text('private-read-token');Path(self.data['argo_token_file']).chmod(0o600)
        app={'spec':{'project':'default','source':{'repoURL':self.data['runtime_repo_url'],'targetRevision':'main','path':'workload'},'destination':{'server':'https://kubernetes.default.svc','namespace':'testlab-rules'}},'status':{'sync':{'status':'Synced'},'health':{'status':'Healthy'}}}
        with patch.object(gitops,'guard_namespace'),patch.object(gitops,'owned_gitlab_ca',return_value=self.work/'tls.crt'),patch.object(gitops,'kubectl',return_value=json.dumps({'data':{'host.docker.internal':'owned certificate fixture'}})),patch.object(gitops,'wait_argo_api'),patch.object(gitops,'argo_http',return_value=app) as read:
            gitops.validate_chain_bootstrap(self.data)
            read.assert_called_once_with(self.data,'GET','/api/v1/applications/testlab-rules',token='private-read-token')
            app['status']['conditions']=[{'type':'ComparisonError'}]
            with self.assertRaises(gitops.BootstrapBlocked):gitops.validate_chain_bootstrap(self.data)


if __name__=='__main__':unittest.main()

import json, sys, tempfile, types, unittest
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import jenkins_gate as gate

class JenkinsGateTests(unittest.TestCase):
    def state(self):
        return {'owner':'testlab-0123456789ab','run_id':'a'*32,'container':'aiops-testlab-0123456789ab-jenkins-'+('a'*12),'image_id':'sha256:owned'}
    def info(self,state):
        return {'Name':'/'+state['container'],'Image':state['image_id'],'Config':{'Image':gate.IMAGE,'Labels':{gate.LABEL:state['owner'],gate.RUN_LABEL:state['run_id']}}}
    def test_cleanup_refuses_owner_run_name_or_image_drift(self):
        state=self.state()
        for field in ('name','image','owner','run','tag'):
            info=self.info(state)
            if field=='name':info['Name']='/other'
            if field=='image':info['Image']='sha256:foreign'
            if field=='owner':info['Config']['Labels'][gate.LABEL]='other'
            if field=='run':info['Config']['Labels'][gate.RUN_LABEL]='other'
            if field=='tag':info['Config']['Image']='jenkins/jenkins:latest'
            with self.subTest(field=field),patch.object(gate,'_inspect',return_value=info),patch.object(gate,'command') as command:
                with self.assertRaises(gate.Blocked):gate.cleanup(state)
                command.assert_not_called()
    def test_cleanup_removes_only_exact_owned_controller_and_its_anonymous_volume(self):
        state=self.state()
        with patch.object(gate,'_inspect',return_value=self.info(state)),patch.object(gate,'command') as command:
            gate.cleanup(state)
        command.assert_called_once_with(['docker','rm','-f','-v',state['container']],timeout=20)
    def test_live_go_gate_cannot_accept_package_only_skip_or_multiple_runs(self):
        for frames in ([{'Action':'pass','Package':'connectors'}],[{'Action':'skip','Test':gate.TEST}],[],[{'Action':'pass','Test':gate.TEST}]*2,[{'Action':'fail','Test':gate.TEST}]):
            with self.assertRaises(AssertionError):gate.original_go_result(types.SimpleNamespace(returncode=0,stdout='\n'.join(json.dumps(x) for x in frames)))
        gate.original_go_result(types.SimpleNamespace(returncode=0,stdout=json.dumps({'Action':'pass','Test':gate.TEST})))
        with self.assertRaises(AssertionError):gate.original_go_result(types.SimpleNamespace(returncode=1,stdout=json.dumps({'Action':'pass','Test':gate.TEST})))
    def test_controlled_build_must_be_unique_successful_complete_and_exact_job(self):
        build={'number':1,'result':'SUCCESS','building':False,'timestamp':1000,'duration':1};view={'fullName':'owned','builds':[build]}
        self.assertEqual(gate.validate_build(view,'owned'),build)
        for changed in ({'fullName':'foreign'},{'builds':[]},{'builds':[build,build]},{'builds':[{**build,'building':True}]},{'builds':[{**build,'result':'FAILURE'}]},{'builds':[{**build,'number':2}]}):
            with self.assertRaises(AssertionError):gate.validate_build({**view,**changed},'owned')
    def test_path_boundary_and_redirect_rejection(self):
        for path in ('https://external.invalid','//external.invalid/path'):
            with self.assertRaises(gate.Blocked):gate.request(path)
        self.assertIsNone(gate.NoRedirect().redirect_request(None,None,302,'',None,'https://external.invalid'))
    def test_sigterm_restores_handler_and_never_signals_saved_pid(self):
        def previous(*args):pass
        class Lock:
            def __enter__(self):return self
            def __exit__(self,*args):return False
        with patch.object(sys,'argv',['jenkins_gate.py','run']),patch.object(gate.signal,'signal',return_value=previous) as signal,patch.object(gate,'exclusive',return_value=Lock()),patch.object(gate,'run',return_value=0):
            self.assertEqual(gate.main(),0)
        self.assertEqual(signal.call_args.args,(gate.signal.SIGTERM,previous))
    def test_init_has_no_external_scm_agents_or_credentials_output(self):
        source=(Path(gate.__file__).parent/'fixtures/jenkins/init.groovy').read_text()
        self.assertIn("permission == Jenkins.READ || permission == Item.READ",source)
        self.assertIn('setSlaveAgentPort(-1)',source)
        self.assertIn('testlab-controlled-build-',source)
        for excluded in ('println token','GitSCM','https://','docker.sock','Pipeline'):
            self.assertNotIn(excluded,source)
    def test_init_permission_type_uses_actual_jenkins_core_namespace(self):
        source=(Path(gate.__file__).parent/'fixtures/jenkins/init.groovy').read_text()
        self.assertIn('import hudson.security.Permission\n',source)
        self.assertNotIn('import hudson.model.Permission',source)

    def test_serialization_allowlist_is_only_our_stateless_fixture_strategy(self):
        self.assertIn('-Dhudson.remoting.ClassFilter=TestlabReadOnlyStrategy',gate.JAVA_OPTIONS)
        self.assertNotIn('SUPPRESS',gate.JAVA_OPTIONS)
        self.assertNotIn('ClassFilter=*',gate.JAVA_OPTIONS)
        self.assertNotIn('ClassFilter=NONE',gate.JAVA_OPTIONS)

    def test_read_access_includes_discovery_required_by_stapler_target(self):
        source=(Path(gate.__file__).parent/'fixtures/jenkins/init.groovy').read_text()
        self.assertIn('permission == Item.DISCOVER',source)
        self.assertNotIn('permission == Item.BUILD',source)
        self.assertNotIn('permission == Item.CONFIGURE',source)

    def test_first_term_during_diagnostics_still_removes_owned_container(self):
        state=self.state()
        with tempfile.TemporaryDirectory() as directory,patch.object(gate,'_interrupted',False),patch.object(gate,'_inspect',side_effect=lambda name:(gate.interrupted(),self.info(state))[1]),patch.object(gate,'_private'),patch.object(gate.subprocess,'run',return_value=types.SimpleNamespace(stdout='',stderr='')),patch.object(gate,'cleanup') as cleanup:
            gate.finalize(state,Path(directory))
            cleanup.assert_called_once_with(state)
            self.assertTrue(gate._interrupted)
            self.assertFalse(gate._cleaning)

    def test_failed_diagnostics_cannot_skip_exact_cleanup(self):
        with patch.object(gate,'_inspect',side_effect=OSError('synthetic inspect failure')),patch.object(gate,'cleanup') as cleanup:
            gate.finalize(self.state(),Path('/unused'))
            cleanup.assert_called_once_with(self.state())

if __name__=='__main__':unittest.main()

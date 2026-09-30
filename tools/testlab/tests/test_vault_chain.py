import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import vault_chain as vault
import identity


class VaultProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name).resolve()
        owner='testlab-abcdef123456';work=self.root/'.local/testlab/environments'/owner
        binary=work/'backend/aiops';binary.parent.mkdir(parents=True);binary.write_bytes(b'owned binary')
        self.backend=dict(owner=owner,binary=str(binary),source_tree_sha='frozen',environment='test',postgres_container='aiops-'+owner+'-pg')
        self.component=dict(owner=owner,work=str(work),credentials={'vault_token':'synthetic-root-do-not-print'})
        self.chain=vault.Chain(self.root,self.backend,self.component,'a'*32)
        self.info=dict(State={'Running':True},Config={'Labels':{'aiops.testlab.owner':owner}},NetworkSettings={'Ports':{'8200/tcp':[{'HostIp':'127.0.0.1','HostPort':'18572'}]}})

    def test_six_binding_dimensions_mutated_individually(self):
        binding=dict(tier='nonprod',environment_id='test',connector_id='c'*32,provider='prometheus',endpoint=vault.ENDPOINT,credential_ref=self.chain.ref)
        mutations=vault.binding_mutations(binding)
        self.assertEqual({name for name,row in mutations},set(binding))
        for field,changed in mutations:self.assertEqual([k for k in binding if binding[k]!=changed[k]],[field])

    def test_blocked_main_exit_code_distinguishes_environment(self):
        with patch.object(sys, 'argv', ['vault_chain.py', 'chain', '--root', str(self.root)]), patch.object(vault, 'doctor', side_effect=identity.Blocked('resource gate')), patch.object(identity, 'emit'):
            self.assertEqual(vault.main(), 2)

    def test_private_state_tracks_exact_resources_before_provisioning(self):
        path=self.chain.work/'state.json';self.assertEqual(path.stat().st_mode&0o777,0o600)
        raw=path.read_text();self.assertNotIn(self.chain.root_token,raw);self.assertNotIn(self.chain.marker,raw)
        self.assertEqual(json.loads(raw)['secret_path'],'testlab-credentials/'+'a'*32)

    def test_root_credential_never_sent_after_owner_or_port_changes(self):
        foreign=dict(self.info,Config={'Labels':{'aiops.testlab.owner':'other'}})
        for info in [None,foreign,dict(self.info,State={'Running':False}),dict(self.info,NetworkSettings={'Ports':{}})]:
            with patch.object(identity,'budgeted_inspect',return_value=info),patch.object(identity,'transport') as send,self.assertRaises(identity.Blocked):
                self.chain.vault('GET','/v1/secret/data/'+self.chain.secret_path)
            send.assert_not_called()

    def test_vault_transport_fixed_origin_and_explicit_token(self):
        with patch.object(identity,'budgeted_inspect',return_value=self.info),patch.object(identity,'transport',return_value=(200,b'{}',{})) as send:
            self.chain.vault('GET','/v1/secret/data/'+self.chain.secret_path,token='limited-read')
        args,kwargs=send.call_args
        self.assertTrue(args[2].startswith(vault.VAULT+'/v1/'))
        self.assertEqual(args[4]['X-Vault-Token'],'limited-read');self.assertEqual(kwargs['origins'],(vault.VAULT,))
        with self.assertRaises(identity.Blocked):self.chain.vault('GET','/v1/../../business')

    def test_formal_api_not_direct_resolver_and_no_secret_in_matrix(self):
        response={'error':{'code':'credential_unavailable','message':'private field missing'}}
        with patch.object(identity,'transport',return_value=(503,json.dumps(response).encode(),{})),patch.object(identity,'emit') as emit:
            self.chain.scoped('POST','/api/v1/connectors/c/activate',{},want=503,code='credential_unavailable',scenario='failure')
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertEqual(row['test_name'],'FormalVaultCredentialBinding')
        self.assertEqual(row['path'],'/api/v1/connectors/c/activate')
        self.assertEqual(row['actual']['error_code'],'credential_unavailable');self.assertEqual(row['scenario'],'failure')
        self.assertNotIn(self.chain.root_token,json.dumps(row));self.assertNotIn('private field',json.dumps(row))
        self.assertEqual(emit.call_args.kwargs['module'],'formal-vault')

    def test_connector_path_uses_inventory_id_placeholder(self):
        self.chain.user_id='c'*32
        with patch.object(identity,'transport',return_value=(200,b'{}',{})),patch.object(identity,'emit'):
            self.chain.scoped('GET','/api/v1/connectors/'+self.chain.user_id)
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text())
        self.assertEqual(row['path'],'/api/v1/connectors/{id}')

    def test_response_leaks_fail_instead_of_success(self):
        with patch.object(identity,'transport',return_value=(200,json.dumps({'secret':self.chain.marker}).encode(),{})),patch.object(identity,'emit'),self.assertRaises(AssertionError):
            self.chain.scoped('GET','/api/v1/connectors/c')

    def test_failed_activation_must_leave_disabled_and_zero_upstream(self):
        self.chain.proxy=Mock();before={'accepted':0};self.chain.proxy.counts.return_value=before
        with patch.object(self.chain,'sql',return_value='f'):self.chain.assert_disabled_without_upstream(before)
        with patch.object(self.chain,'sql',return_value='t'),self.assertRaises(AssertionError):self.chain.assert_disabled_without_upstream(before)
        self.chain.proxy.counts.return_value={'accepted':1}
        with self.assertRaises(AssertionError):self.chain.assert_disabled_without_upstream(before)

    def test_cleanup_only_this_run_resources_and_retains_audit(self):
        self.chain.connector_id='d'*32;self.chain.seeded=True;self.chain.tokens=['limited-one','limited-two'];self.chain.policies=['testlab-'+self.chain.run_id+'-read']
        with patch.object(self.chain,'sql') as sql,patch.object(self.chain,'vault') as calls,patch.object(identity,'emit'):
            self.chain.cleanup()
        statement=sql.call_args.args[0]
        self.assertIn("WHERE id='"+self.chain.connector_id+"' AND endpoint='"+vault.ENDPOINT+"'",statement)
        self.assertNotIn('management_audit',statement);self.assertNotIn('TRUNCATE',statement)
        for call in calls.call_args_list:
            method,path=call.args[:2]
            self.assertTrue(path=='/v1/auth/token/revoke' or self.chain.run_id in path)
        self.assertEqual(sum(call.args[1]=='/v1/auth/token/revoke' for call in calls.call_args_list),2)

    def test_doctor_blocks_before_ports_or_credentials_without_attested_base(self):
        with patch.object(vault,'attest',side_effect=identity.Blocked('stale source')),patch.object(identity,'free_port') as port,self.assertRaises(identity.Blocked):vault.doctor(self.root)
        port.assert_not_called()


if __name__=='__main__':unittest.main()

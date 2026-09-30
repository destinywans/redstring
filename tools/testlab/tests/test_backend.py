import sys
from pathlib import Path
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import backend


class ControllerAttestationTests(unittest.TestCase):
    def test_exact_live_identity_signature_is_required(self):
        nonce='a'*32
        current={'owner':'testlab-private','pid':123,'api_pids':[124,125],'ready':True}
        secret='generated-private-controller-secret'
        record={'nonce':nonce,'identity':current,'proof':backend.control_proof(secret,nonce,current)}
        with patch.object(backend.secrets,'token_hex',return_value=nonce),patch.object(backend,'get',return_value=record):
            self.assertEqual(backend.attest({'control_secret':secret}),current)
        for forged in [dict(record,nonce='b'*32),dict(record,identity=dict(current,pid=999)),dict(record,proof=backend.control_proof('foreign',nonce,current)),dict(record,proof=None)]:
            with self.subTest(forged=forged),patch.object(backend.secrets,'token_hex',return_value=nonce),patch.object(backend,'get',return_value=forged):
                with self.assertRaisesRegex(ValueError,'attestation failed'):
                    backend.attest({'control_secret':secret})

    def test_invalid_challenge_cannot_be_signed(self):
        for nonce in ['','../secret','a'*31,'a'*33,'A'*32,None]:
            with self.subTest(nonce=nonce),self.assertRaises(ValueError):
                backend.control_proof('private',nonce,{'pid':123})

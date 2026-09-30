"""Offline orchestration checks only: no fixture preparation or real load."""
import sys,unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import diagnose


class DiagnosticEntryTest(unittest.TestCase):
    def setUp(self):
        self.data={'counts':{'exact_profile':True,'profile':'base'}}
        self.guard={'_freeze_baseline':(1,(2,3),'hash')}
        self.streams=Mock();self.streams.snapshot.return_value={'peak_normal':50,'peak_slow':10}
        self.records=[]
        def record(identifier,status,actual,*args,**kwargs):self.records.append((identifier,status,actual))
        self.patches=[patch.object(diagnose.perf,'fixture',return_value=self.guard),patch.object(diagnose.perf,'frozen',return_value='fixed'),patch.object(diagnose.perf,'prepare',return_value=self.data),patch.object(diagnose.perf,'Streams',return_value=self.streams),patch.object(diagnose.perf,'metrics',return_value={'at':1}),patch.object(diagnose.perf,'k6',return_value=(True,{})),patch.object(diagnose.perf,'resource_recovery',return_value=(True,{})),patch.object(diagnose.perf,'consistency',return_value=True),patch.object(diagnose.perf,'sse_accepted',return_value=True),patch.object(diagnose.perf,'result',side_effect=record),patch.object(diagnose.subprocess,'run',return_value=SimpleNamespace(returncode=0,stdout='k6 v1.5.0')),patch.object(diagnose.signal,'signal')]
        for item in self.patches:item.start()
        for item in self.patches:self.addCleanup(item.stop)
    def assert_scope(self):
        self.assertTrue(self.records)
        for _,_,actual in self.records:
            self.assertTrue(actual['diagnostic_only'])
            self.assertFalse(actual['formal_performance_acceptance'])
            self.assertFalse(actual['formal_24h_acceptance'])
    def test_fixed_base_sequence_original_streams_and_cleanup(self):
        original_result,original_frozen=diagnose.perf.result,diagnose.perf.frozen
        self.assertEqual(diagnose.main([]),0)
        diagnose.perf.prepare.assert_called_once_with('base')
        self.assertEqual(self.data['_freeze_baseline'],self.guard['_freeze_baseline'])
        self.assertEqual([(call.args[2],call.args[3],call.args[4],call.args[5]) for call in diagnose.perf.k6.call_args_list],[('warmup',10,120,False),('rps10',10,300,False),('rps25',25,300,True)])
        for call in diagnose.perf.k6.call_args_list:self.assertIs(call.args[6],self.streams)
        self.assertEqual(diagnose.perf.Streams.call_args.kwargs,{'normal':50,'slow':10,'reconnect_seconds':60})
        self.streams.start.assert_called_once();self.streams.close.assert_called_once()
        diagnose.perf.resource_recovery.assert_called_once();diagnose.perf.consistency.assert_called_once()
        self.assertIs(diagnose.perf.result,original_result);self.assertIs(diagnose.perf.frozen,original_frozen)
        self.assert_scope()
    def test_stage_failure_preserved_and_later_stages_cannot_make_green(self):
        diagnose.perf.k6.side_effect=[(False,{'first_failure':'preserved'}),(True,{}),(True,{})]
        self.assertEqual(diagnose.main([]),1)
        self.assertEqual(diagnose.perf.k6.call_count,3)
        self.assertEqual(self.records[-1][0:2],('diagnostic.summary','failed'))
        self.streams.close.assert_called_once();self.assert_scope()
    def test_stage_exception_and_interrupt_cleanup_nonzero(self):
        for error,code,status in ((RuntimeError('synthetic'),2,'failed'),(KeyboardInterrupt(),130,'interrupted')):
            with self.subTest(error=type(error).__name__):
                self.streams.close.reset_mock();diagnose.perf.k6.side_effect=error
                self.assertEqual(diagnose.main([]),code)
                self.streams.close.assert_called_once();self.assertEqual(self.records[-1][1],status)
                self.assert_scope()
    def test_cleanup_failure_is_visible_and_cannot_return_success(self):
        diagnose.perf.k6.side_effect=RuntimeError('stage failure');self.streams.close.side_effect=RuntimeError('cleanup failure')
        self.assertEqual(diagnose.main([]),2)
        self.assertEqual(self.records[-1][0:2],('diagnostic.sse_cleanup','failed'));self.assert_scope()
    def test_stream_start_failure_also_cleans_constructed_streams(self):
        self.streams.start.side_effect=RuntimeError('start failure')
        self.assertEqual(diagnose.main([]),2)
        self.streams.close.assert_called_once();diagnose.perf.k6.assert_not_called();self.assert_scope()
    def test_recovery_or_consistency_failure_keeps_summary_failed(self):
        diagnose.perf.resource_recovery.return_value=(False,{})
        self.assertEqual(diagnose.main([]),1)
        diagnose.perf.resource_recovery.return_value=(True,{});diagnose.perf.consistency.return_value=False
        self.assertEqual(diagnose.main([]),1)
        self.assertEqual(self.records[-1][0:2],('diagnostic.summary','failed'));self.assert_scope()
    def test_larger_profile_or_unavailable_version_never_starts_streams(self):
        self.data['counts']['exact_profile']=False
        self.assertEqual(diagnose.main([]),2);diagnose.perf.Streams.assert_not_called();diagnose.perf.k6.assert_not_called()
        self.data['counts']['exact_profile']=True;diagnose.subprocess.run.return_value=SimpleNamespace(returncode=0,stdout='k6 v9.0.0')
        self.assertEqual(diagnose.main([]),2);diagnose.perf.Streams.assert_not_called();self.assert_scope()
    def test_result_values_and_helper_freeze_preserved(self):
        old_result,old_frozen=diagnose.perf.result,diagnose.perf.frozen
        original={'error':'original error','nested':{'first':'failure'}}
        with diagnose.diagnostic_scope():
            diagnose.perf.result('original','failed',original)
            diagnose.perf.result('original-string','failed','original string error')
            with patch.object(diagnose.hashlib,'sha256') as sha:
                sha.return_value.hexdigest.return_value='changed'
                with self.assertRaisesRegex(RuntimeError,'helper changed'):diagnose.perf.frozen(self.data)
        self.assertEqual(self.records[-2][2]['error'],original['error'])
        self.assertIs(self.records[-2][2]['nested'],original['nested'])
        self.assertEqual(self.records[-1][2]['detail'],'original string error')
        self.assertIs(diagnose.perf.result,old_result);self.assertIs(diagnose.perf.frozen,old_frozen)
        self.assertEqual(original,{'error':'original error','nested':{'first':'failure'}});self.assert_scope()
    def test_sigterm_during_final_handoff_cannot_report_passed(self):
        self.addCleanup(diagnose.perf.STOP.clear)
        def terminate():
            handler=[call.args[1] for call in diagnose.signal.signal.call_args_list if call.args[0]==diagnose.signal.SIGTERM][-1]
            handler(diagnose.signal.SIGTERM,None)
        for phase in ('consistency','final_frozen'):
            with self.subTest(phase=phase):
                diagnose.perf.STOP.clear();self.records.clear()
                diagnose.perf.consistency.side_effect=None;diagnose.perf.frozen.side_effect=None
                if phase=='consistency':
                    def consistent(data):terminate();return True
                    diagnose.perf.consistency.side_effect=consistent
                else:
                    calls=0
                    def frozen(data):
                        nonlocal calls
                        calls+=1
                        if calls==3:terminate()
                        return 'fixed'
                    diagnose.perf.frozen.side_effect=frozen
                original_result,original_frozen=diagnose.perf.result,diagnose.perf.frozen
                self.assertEqual(diagnose.main([]),130)
                self.assertNotIn(('diagnostic.summary','passed'),[(identifier,status) for identifier,status,_ in self.records])
                self.assertEqual(self.records[-1][0:2],('diagnostic.interrupted','interrupted'))
                self.assertIs(diagnose.perf.result,original_result);self.assertIs(diagnose.perf.frozen,original_frozen)
                self.assert_scope()
    def test_sigterm_between_stages_never_starts_next_load(self):
        diagnose.perf.STOP.clear();self.addCleanup(diagnose.perf.STOP.clear)
        def first_stage(*args,**kwargs):
            handler=[call.args[1] for call in diagnose.signal.signal.call_args_list if call.args[0]==diagnose.signal.SIGTERM][-1]
            handler(diagnose.signal.SIGTERM,None);return True,{}
        diagnose.perf.k6.side_effect=first_stage
        self.assertEqual(diagnose.main([]),130)
        self.assertEqual(diagnose.perf.k6.call_count,1);self.streams.close.assert_called_once()
        self.assertEqual(self.records[-1][0:2],('diagnostic.interrupted','interrupted'));self.assert_scope()


if __name__=='__main__':unittest.main()

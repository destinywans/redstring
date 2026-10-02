"""Offline qualification regressions, synthetic summaries only; not 12h/24h proof."""
import copy,sys,unittest
from contextlib import ExitStack
from pathlib import Path
from unittest.mock import patch,Mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import perf

class SoakQualificationTest(unittest.TestCase):
    trial_start=1790726400.0
    def summary(self,delay=1):
        s=round((self.trial_start+delay)*1000)
        def gauge(low,high=None):return {'type':'gauge','values':{'min':low,'max':low if high is None else high}}
        def counter(n):return {'type':'counter','values':{'count':n}}
        return {'state':{'testRunDurationMs':86400700},'soak_evidence':{'schema':1,'scenario':'traffic',
                'executor':'constant-arrival-rate','duration_seconds':86400,'rate_rps':10,'force_relogin':False},
                'metrics':{'soak_scenario_start_ms':gauge(s),'soak_test_start_lower_ms':gauge(s-501),
                'soak_test_start_upper_ms':gauge(s-499),'soak_iteration_finished_ms':gauge(s+86399950),
                'session_relogins':counter(80),'soak_authenticated_after_12h':counter(430000),
                'soak_renewed_at_ms':gauge(s+42900001,s+85800001),
                'soak_renewed_elapsed_ms':gauge(42900001,85800001),'soak_renewal_ttl_seconds':gauge(43200),
                'soak_renewal_due_offset_ms':gauge(1,10)}}
    def qualify(self,summary):return perf.qualify_soak_summary(summary,10,86400)
    def finish(self,start,end):
        row={'trial_id':'a'*32,'status':'completed','started_at':perf.datetime.datetime.fromtimestamp(self.trial_start,perf.datetime.timezone.utc).isoformat(),
             'expires_at':perf.datetime.datetime.fromtimestamp(self.trial_start+86400,perf.datetime.timezone.utc).isoformat(),
             'evaluated_points':1440,'missing_points':0}
        data={'base_urls':['http://synthetic.invalid'],'hook_user':{'cookie':'synthetic'}}
        with patch.object(perf,'frozen'),patch.object(perf,'request',return_value=(200,row,[])):
            return perf.finish_trial(data,{'trial_status_path':'/api/v1/alert-rules/dry-runs/'+row['trial_id']},start,end)
    def test_good_natural_24h_summary_has_bounded_execution_and_real_js_renewals(self):
        q=self.qualify(self.summary());self.assertTrue(q['accepted'],q)
        self.assertEqual(q['renewal']['js_relogins'],80);self.assertEqual(q['renewal']['ttl_seconds_min'],43200)
        self.assertEqual(q['window']['arrival_duration_seconds_configured'],86400)
        self.assertGreater(q['window']['engine_duration_seconds_including_graceful_stop'],86400)
    def test_original_a1_counterexample_wrapper_one_second_real_start_sixty_one(self):
        legacy,_=self.finish(self.trial_start+1,self.trial_start+86441);self.assertTrue(legacy)
        q=self.qualify(self.summary(delay=61));self.assertTrue(q['accepted'],q)
        window=q['window'];current,evidence=self.finish(window['scenario_started_at_upper'],window['engine_finished_at_lower'])
        self.assertFalse(current);self.assertGreater(evidence['start_alignment_seconds'],60)
        self.assertLess(evidence['load_finished_at'],self.trial_start+86500)
    def test_strict_alignment_preserves_upper_bound_without_tolerance(self):
        q=self.qualify(self.summary(delay=59.999));window=q['window']
        current,_=self.finish(window['scenario_started_at_upper'],window['engine_finished_at_lower']);self.assertTrue(current)
        q=self.qualify(self.summary(delay=60));window=q['window']
        current,_=self.finish(window['scenario_started_at_upper'],window['engine_finished_at_lower']);self.assertFalse(current)
    def test_python_hook_count_cannot_substitute_for_zero_js_relogin_evidence(self):
        s=self.summary();s['hooks']={'relogins':21};s['metrics']['session_relogins']['values']['count']=0
        self.assertFalse(self.qualify(s)['accepted'])
    def test_no_successful_authenticated_request_after_twelve_hours_is_rejected(self):
        s=self.summary();s['metrics']['soak_authenticated_after_12h']['values']['count']=0
        self.assertFalse(self.qualify(s)['accepted'])
    def test_every_required_metric_is_fail_closed_when_missing(self):
        original=self.summary()
        for name in original['metrics']:
            with self.subTest(name=name):
                s=copy.deepcopy(original);del s['metrics'][name];self.assertFalse(self.qualify(s)['accepted'])
    def test_forced_schedule_wrong_ttl_early_only_or_clock_inconsistency_is_rejected(self):
        mutations=[lambda s:s['soak_evidence'].update(force_relogin=True),
          lambda s:s['metrics']['soak_renewal_ttl_seconds']['values'].update(min=3600,max=3600),
          lambda s:s['metrics']['soak_renewal_due_offset_ms']['values'].update(min=-1),
          lambda s:s['metrics']['soak_renewed_elapsed_ms']['values'].update(max=42900001),
          lambda s:s['metrics']['soak_renewed_at_ms']['values'].update(max=s['metrics']['soak_renewed_at_ms']['values']['max']+1),
          lambda s:s['metrics']['soak_test_start_upper_ms']['values'].update(max=s['metrics']['soak_test_start_lower_ms']['values']['min']+1001)]
        for mutate in mutations:
            s=self.summary();mutate(s);self.assertFalse(self.qualify(s)['accepted'])
    def test_nonfinite_boolean_wrong_type_or_unordered_metrics_are_rejected(self):
        for value in (float('nan'),float('inf'),True,None):
            s=self.summary();s['metrics']['soak_scenario_start_ms']['values']['min']=value
            self.assertFalse(self.qualify(s)['accepted'])
        s=self.summary();s['metrics']['soak_scenario_start_ms']['type']='trend';self.assertFalse(self.qualify(s)['accepted'])
        s=self.summary();s['metrics']['soak_renewed_elapsed_ms']['values'].update(min=90000000,max=85800001);self.assertFalse(self.qualify(s)['accepted'])
    def test_main_wires_generator_bounds_and_rejects_missing_qualification(self):
        qualified=self.qualify(self.summary())
        for qualification,exit_code in ((qualified,0),(None,1)):
            data={'counts':{'profile':'base','exact_profile':True},'perf_work':'/private/tmp/synthetic-soak-unused'}
            actual={'generator_seconds':86400,'hooks':{'dryrun_calls':1,'webhook_calls':1,'chat_turns':1,'relogins':21}}
            if qualification is not None:actual['soak_qualification']=qualification
            stream=Mock();stream.snapshot.return_value={'peak_normal':2,'peak_slow':1,'reconnects':1}
            finish=Mock(return_value=(True,{}));result=Mock()
            replacements={'fixture':Mock(return_value=data),'frozen':Mock(),'hooks_config':Mock(return_value={}),
                          'prepare':Mock(return_value=data),'metrics':Mock(return_value={}),
                          'Streams':Mock(return_value=stream),'start_trial':Mock(),
                          'k6':Mock(return_value=(True,actual)),'finish_trial':finish,
                          'sse_accepted':Mock(return_value=True),'consistency':Mock(return_value=True),
                          'resource_recovery':Mock(return_value=(True,{})),'result':result,
                          'cancel_requested':Mock(return_value=False)}
            with ExitStack() as stack:
                for name,replacement in replacements.items():stack.enter_context(patch.object(perf,name,replacement))
                stack.enter_context(patch.object(perf.sys,'argv',['perf.py','soak','--profile','base','--duration','86400']))
                stack.enter_context(patch.object(perf.signal,'signal'))
                stack.enter_context(patch.object(perf.subprocess,'run',return_value=Mock(returncode=0,stdout='k6 v1.5.0')))
                stack.enter_context(patch.object(perf.time,'time',return_value=self.trial_start+1))
                self.assertEqual(perf._main(),exit_code)
            expected=qualification['window'] if qualification is not None else {}
            self.assertEqual(finish.call_args.args[2:],(expected.get('scenario_started_at_upper',0),expected.get('engine_finished_at_lower',0)))
            terminal=[call for call in result.call_args_list if call.args[0]=='perf.soak_acceptance'][-1]
            self.assertEqual(terminal.args[1],'passed' if exit_code==0 else 'failed')
            self.assertEqual(actual['trial_final']['python_call_finished_at'],self.trial_start+1)

    def test_real_duration_profile_and_rate_are_not_relaxed(self):
        s=self.summary();s['state']['testRunDurationMs']=86399999;self.assertFalse(self.qualify(s)['accepted'])
        for rate,duration in ((10,86399),(25,86400),(10,12)):
            self.assertFalse(perf.qualify_soak_summary(self.summary(),rate,duration)['accepted'])
        s=self.summary();s['soak_evidence']['duration_seconds']=86399;self.assertFalse(self.qualify(s)['accepted'])
        s=self.summary();s['soak_evidence']['rate_rps']=9;self.assertFalse(self.qualify(s)['accepted'])

if __name__=='__main__':unittest.main()

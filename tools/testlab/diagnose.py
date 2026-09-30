"""Fixed base-profile diagnostic, never full performance or 24-hour acceptance."""
from __future__ import annotations
import argparse,hashlib,signal,subprocess,sys,time,uuid
from contextlib import contextmanager
from pathlib import Path
import perf

STAGES=(('warmup',10,120,False),('rps10',10,300,False),('rps25',25,300,True))
SCOPE={'diagnostic_only':True,'formal_performance_acceptance':False,'formal_24h_acceptance':False}


def check_stop():
    if perf.STOP.is_set():raise KeyboardInterrupt()


@contextmanager
def diagnostic_scope():
    original_result,original_frozen=perf.result,perf.frozen
    script=Path(__file__).resolve();fingerprint=hashlib.sha256(script.read_bytes()).hexdigest()
    def scoped_result(identifier,status,actual,*args,**kwargs):
        actual=dict(actual) if isinstance(actual,dict) else {'detail':actual}
        original_result(identifier,status,{**actual,**SCOPE},*args,**kwargs)
    def frozen(data):
        if script.is_symlink() or hashlib.sha256(script.read_bytes()).hexdigest()!=fingerprint:
            raise RuntimeError('diagnostic helper changed; restart/re-freeze required')
        return original_frozen(data)
    perf.result,perf.frozen=scoped_result,frozen
    try:yield
    finally:perf.result,perf.frozen=original_result,original_frozen


def run():
    started=time.monotonic();streams=None
    try:
        check_stop();guard=perf.fixture();perf.frozen(guard);check_stop()
        data=perf.prepare('base');check_stop();data['_freeze_baseline']=guard['_freeze_baseline'];perf.frozen(data);check_stop()
        if not data['counts']['exact_profile']:
            raise RuntimeError('diagnostic requires exact base profile; larger owned profile is not a base gate')
        perf.result('diagnostic.seed','passed',data['counts'],started,expected='独立基础档真实数据与授权预检；不能替代完整性能验收')
        version=subprocess.run(['docker','run','--rm',perf.K6_IMAGE,'version'],capture_output=True,text=True,timeout=30)
        check_stop()
        if version.returncode or 'v1.5.0' not in version.stdout:raise RuntimeError('pinned k6 1.5.0 not available')
        identifier=uuid.uuid4().hex[:12]
        streams=perf.Streams(data,identifier,normal=50,slow=10,reconnect_seconds=60)
        baseline=perf.metrics(data);check_stop();streams.start();good=True
        for stage,rate,seconds,gate in STAGES:
            check_stop()
            passed,_=perf.k6(data,identifier,stage,rate,seconds,gate,streams)
            check_stop()
            good=good and passed
        stats=streams.snapshot()
        sse_good=stats['peak_normal']==50 and stats['peak_slow']==10 and perf.sse_accepted(stats)
        perf.result('diagnostic.sse','passed' if sse_good else 'failed',stats,started,expected='原50正常/10慢读、60秒cursor重连；真实SSE诊断')
        streams.close();streams=None
        check_stop()
        recovered,_=perf.resource_recovery(data,baseline,started)
        check_stop();consistent=perf.consistency(data);check_stop();perf.frozen(data);check_stop()
        accepted=good and sse_good and recovered and consistent
        perf.result('diagnostic.summary','passed' if accepted else 'failed',{'profile':'base','stages':[{'stage':stage,'rate_rps':rate,'planned_seconds':seconds,'gate':gate} for stage,rate,seconds,gate in STAGES],'diagnostic_passed':accepted,'scope':'three original base stages only; excludes rps50, burst100, scale and soak'},started)
        return 0 if accepted else 1
    except KeyboardInterrupt:
        perf.result('diagnostic.interrupted','interrupted',{'acceptance':False},started);return 130
    except Exception as error:
        perf.result('diagnostic.failure','failed',{'error_type':type(error).__name__,'acceptance':False},started);return 2
    finally:
        if streams is not None:
            try:streams.close()
            except Exception as error:
                # An earlier failure remains a nonzero return; no cleanup failure is hidden.
                perf.result('diagnostic.sse_cleanup','failed',{'error_type':type(error).__name__,'acceptance':False},started)


def main(argv=None):
    argparse.ArgumentParser(description=__doc__).parse_args(argv)
    signal.signal(signal.SIGINT,lambda *args:perf.STOP.set())
    signal.signal(signal.SIGTERM,lambda *args:perf.STOP.set())
    with diagnostic_scope():return run()


if __name__=='__main__':sys.exit(main())

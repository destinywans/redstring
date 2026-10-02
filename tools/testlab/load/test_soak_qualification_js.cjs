// Offline mocked k6 ABI; never starts k6/HTTP. Synthetic clocks are only test inputs.
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const raw=fs.readFileSync(__dirname+'/traffic.js','utf8');
const source=raw.replace(/^import .*;\n/gm,'').replace('export default function(){','globalThis.runIteration=function(){').replace('export function handleSummary(data){','globalThis.handleSummary=function(data){').replace('export const options=','const options=');
function fixture(soakEnabled){
 const metrics={},calls=[],start=1790726400000;let now=start;
 class Metric{constructor(name){this.name=name;this.values=[];metrics[name]=this;}add(n){this.values.push(n);}}
 const config={soak_qualification:soakEnabled,force_relogin:false,base_urls:['http://127.0.0.1:1'],environment:'test',password:'synthetic',aggregate_measurement_supported:true,
  users:[{email:'synthetic@testlab.invalid',cookie:'initial-synthetic',session_expires_at:(start+43200000)/1000,conversation_id:'own-conv',claim_alert_id:'own-alert',claim_revision:1}]};
 const reply=()=>({status:200,body:'{}',timings:{duration:2}});
 const http={get:(url,opts)=>{calls.push({method:'GET',url,opts});return reply();},post:(url,body,opts)=>{
  calls.push({method:'POST',url,body,opts});return url.endsWith('/auth/login')?{status:200,cookies:{aiops_session:[{value:'renewed-synthetic'}]},headers:{'Set-Cookie':'aiops_session=renewed-synthetic; Max-Age=43200'}}:reply();}};
 const execution={instance:{get currentTestRunDuration(){return now-start;}},scenario:{startTime:start,iterationInTest:0},vu:{idInTest:1}};
 const context={http,execution,Counter:Metric,Gauge:Metric,Rate:Metric,Trend:Metric,Date:{now:()=>now},open:()=>JSON.stringify(config),__ENV:{GATE:'1',RATE:'10',DURATION:'86400s'}};
 vm.createContext(context);vm.runInContext(source,context);
 return {context,metrics,calls,start,setTime:t=>{now=start+t;},config};
}
const soak=fixture(true);soak.context.runIteration();
assert.equal(soak.calls.length,1);assert.equal(soak.calls[0].method,'GET');
assert.deepEqual(soak.metrics.soak_scenario_start_ms.values,[soak.start]);
assert.equal(soak.metrics.session_relogins.values.length,0);
soak.setTime(42900001);soak.context.runIteration();
assert.equal(soak.metrics.session_relogins.values.length,1);
assert.deepEqual(soak.metrics.soak_renewal_ttl_seconds.values,[43200]);
assert.deepEqual(soak.metrics.soak_renewal_due_offset_ms.values,[1]);
assert.equal(soak.metrics.soak_authenticated_after_12h.values.length,0);
soak.setTime(43200001);soak.context.runIteration();assert.equal(soak.metrics.soak_authenticated_after_12h.values.length,0);
soak.setTime(85800002);soak.context.runIteration();assert.equal(soak.metrics.session_relogins.values.length,2);
assert.equal(soak.metrics.soak_authenticated_after_12h.values.length,1);
assert.deepEqual(soak.metrics.soak_renewed_elapsed_ms.values,[42900001,85800002]);
const output=JSON.parse(soak.context.handleSummary({metrics:{},state:{testRunDurationMs:86400000}})['/private/summary.json']);
assert.equal(output.soak_evidence.force_relogin,false);assert.equal(output.soak_evidence.duration_seconds,86400);
const base=fixture(false);base.context.runIteration();base.setTime(42900001);base.context.runIteration();
assert.equal(Object.keys(base.metrics).some(n=>n.startsWith('soak_')),false);
assert.equal(JSON.parse(base.context.handleSummary({metrics:{},state:{}})['/private/summary.json']).soak_evidence,undefined);
assert.deepEqual(JSON.parse(JSON.stringify(base.calls)),JSON.parse(JSON.stringify(soak.calls.slice(0,3))));
// Preserve the original options/thresholds and scripted request shape for base load.
const baseOptions=vm.runInContext('options',base.context),soakOptions=vm.runInContext('options',soak.context);
assert.deepEqual(JSON.parse(JSON.stringify(baseOptions.scenarios)),JSON.parse(JSON.stringify(soakOptions.scenarios)));
const extra=JSON.parse(JSON.stringify(soakOptions.thresholds));delete extra.session_relogins;
assert.deepEqual(extra,JSON.parse(JSON.stringify(baseOptions.thresholds)));
console.log('offline mocked natural renewal and ordinary-load isolation passed; not real k6/12h evidence');

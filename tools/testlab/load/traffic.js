import http from 'k6/http';
import execution from 'k6/execution';
import { Counter, Gauge, Rate, Trend } from 'k6/metrics';
const config=JSON.parse(open('/private/workload.json'));
const errors=new Rate('unexpected_errors');
const writeErrors=new Rate('bounded_write_errors');
const writeLatency=new Trend('bounded_write_latency',true);
const db=new Trend('database_latency',true);
const aggregate=new Trend('aggregate_latency',true);
const relogins=new Counter('session_relogins');
// Only formal soak registers these constant-storage gauges; ordinary load metrics stay unchanged.
const soak=config.soak_qualification?{
 scenarioStart:new Gauge('soak_scenario_start_ms'),testStartLower:new Gauge('soak_test_start_lower_ms'),
 testStartUpper:new Gauge('soak_test_start_upper_ms'),iterationFinished:new Gauge('soak_iteration_finished_ms'),
 renewedAt:new Gauge('soak_renewed_at_ms'),renewedElapsed:new Gauge('soak_renewed_elapsed_ms'),
 renewalTTL:new Gauge('soak_renewal_ttl_seconds'),renewalDueOffset:new Gauge('soak_renewal_due_offset_ms'),
 authenticatedAfter12h:new Counter('soak_authenticated_after_12h')
}:null;
// Fixed route names keep diagnostic evidence independent of IDs, users and URLs.
const routeMetrics={};
for(const route of ['conversation_list','conversation_detail','inbox','alert_detail','alert_claim','alert_groups','home']){
 routeMetrics[route]={latency:new Trend('route_'+route+'_latency',true),errors:new Rate('route_'+route+'_errors'),requests:new Counter('route_'+route+'_requests')};
}
const thresholds={};
if(__ENV.GATE==='1'||__ENV.SELF_CHECK==='1')thresholds.dropped_iterations=['count==0'];
if(__ENV.GATE==='1'||__ENV.SELF_CHECK==='1')thresholds.unexpected_errors=['rate<0.001'];
if(__ENV.GATE==='1'||__ENV.SELF_CHECK==='1')thresholds.bounded_write_errors=['rate<0.001'];
if(config.force_relogin||soak)thresholds.session_relogins=['count>0'];
if(__ENV.GATE==='1'){
 thresholds.database_latency=['p(95)<=500','p(99)<=2000'];
 for(const [path,route] of [['/api/v1/inbox','inbox'],['/api/v1/alert-groups','alert_groups'],['/api/v1/home','home']]){
  if(!(config.excluded_aggregate_paths||[]).includes(path)){
   thresholds['route_'+route+'_latency']=['p(95)<=500','p(99)<=2000'];
   thresholds['route_'+route+'_errors']=['rate<0.001'];
  }
 }
 if(config.aggregate_measurement_supported)thresholds.aggregate_latency=['p(95)<=2000','p(99)<=5000'];
}
export const options={scenarios:{traffic:{executor:'constant-arrival-rate',rate:Number(__ENV.RATE||10),timeUnit:'1s',duration:__ENV.DURATION||'300s',preAllocatedVUs:40,maxVUs:200,gracefulStop:'15s'}},thresholds,summaryTrendStats:['avg','min','med','max','p(95)','p(99)'],discardResponseBodies:true,systemTags:['status','method','name','scenario','expected_response']};
let cookie='',expires=0,soakClockRecorded=false,soakCookieRenewedElapsed=-1;
export default function(){
 if(soak&&!soakClockRecorded){
   const before=Date.now(),elapsed=execution.instance.currentTestRunDuration,after=Date.now();
   soak.scenarioStart.add(execution.scenario.startTime);
   soak.testStartLower.add(before-elapsed-1);soak.testStartUpper.add(after-elapsed+1);soakClockRecorded=true;
 }
 const u=config.users[(execution.vu.idInTest-1)%config.users.length];
 const origin=config.base_urls[execution.scenario.iterationInTest%config.base_urls.length];
 const target=origin.replace('127.0.0.1','host.docker.internal');
 if(!cookie){cookie=u.cookie;expires=config.force_relogin?0:Number(u.session_expires_at)*1000-300000;}
 if(Date.now()>expires){
   const renewalDue=expires,renewalStarted=Date.now();
   const login=http.post(target+'/api/v1/auth/login',JSON.stringify({email:u.email,password:config.password}),{headers:{'Content-Type':'application/json',Origin:origin,'X-Forwarded-For':'192.0.2.'+(1+(execution.vu.idInTest%240))},tags:{name:'relogin'},responseType:'text',timeout:'15s'});
   const c=login.cookies.aiops_session;
   if(login.status!==200||!c||!c.length){errors.add(true);return;}
   const setCookie=String(login.headers['Set-Cookie']||login.headers['set-cookie']||'');const ttl=/Max-Age=(\d+)/i.exec(setCookie);
   if(!ttl||Number(ttl[1])<=300){errors.add(true);return;}
   cookie=c[0].value;expires=Date.now()+(Number(ttl[1])-300)*1000;relogins.add(1);
   if(soak){
     const acceptedAt=Date.now();soakCookieRenewedElapsed=acceptedAt-execution.scenario.startTime;
     soak.renewedAt.add(acceptedAt);soak.renewedElapsed.add(acceptedAt-execution.scenario.startTime);
     soak.renewalTTL.add(Number(ttl[1]));soak.renewalDueOffset.add(renewalStarted-renewalDue);
   }
 }
 const n=execution.scenario.iterationInTest%10;
 const paths=[['/api/v1/conversations','db'],['/api/v1/conversations/'+u.conversation_id,'db'],['/api/v1/inbox','db'],['/api/v1/conversations','db'],['/api/v1/conversations/'+u.conversation_id,'db'],['/api/v1/inbox','db'],['/api/v1/conversations','db'],['/api/v1/conversations/'+u.conversation_id,'db'],['/api/v1/alert-groups','db'],['/api/v1/home','db']];
 for(let i=0;i<paths.length;i++){if((config.excluded_aggregate_paths||[]).includes(paths[i][0]))paths[i]=['/api/v1/alerts/'+u.claim_alert_id,'db'];}
 const [path,kind]=paths[n];
 const boundedWrite=execution.scenario.iterationInTest%25===24;
 const options={headers:{Cookie:'aiops_session='+cookie,'X-AIOps-Environment':config.environment},tags:{name:path.includes('/conversations/')?'conversation_detail':path.includes('/alerts/')?'own_alert_detail':path,kind},timeout:'15s',responseType:'text'};
 const response=boundedWrite?http.post(target+'/api/v1/alerts/'+u.claim_alert_id+'/claim',JSON.stringify({expected_revision:u.claim_revision}),{...options,headers:{...options.headers,'Content-Type':'application/json',Origin:origin},tags:{name:'own_alert_claim_retry',kind:'db_write'}}):http.get(target+path,options);
 // Count a 200 error envelope as a failure as well. No tokens or response bodies are logged.
 let valid=response.status===200;
 if(valid){try{const body=JSON.parse(response.body);valid=!!body&&typeof body==='object'&&!body.error;}catch(_){valid=false;}}
 errors.add(!valid);
 const route=boundedWrite?'alert_claim':path==='/api/v1/conversations'?'conversation_list':path.startsWith('/api/v1/conversations/')?'conversation_detail':path==='/api/v1/inbox'?'inbox':path.startsWith('/api/v1/alerts/')?'alert_detail':path==='/api/v1/alert-groups'?'alert_groups':'home';
 routeMetrics[route].latency.add(response.timings.duration);
 routeMetrics[route].errors.add(!valid);
 routeMetrics[route].requests.add(1);
 if(boundedWrite){writeErrors.add(!valid);writeLatency.add(response.timings.duration);db.add(response.timings.duration);}
 else (kind==='db'?db:aggregate).add(response.timings.duration);
 if(soak){
   const finishedAt=Date.now();soak.iterationFinished.add(finishedAt);
   if(valid&&soakCookieRenewedElapsed>=43200000&&finishedAt-execution.scenario.startTime>=43200000)soak.authenticatedAfter12h.add(1);
 }
}
export function handleSummary(data){
 if(soak)data={...data,soak_evidence:{schema:1,scenario:'traffic',executor:'constant-arrival-rate',
   duration_seconds:Number(String(__ENV.DURATION).replace(/s$/, '')),rate_rps:Number(__ENV.RATE),force_relogin:config.force_relogin===true}};
 if(!config.aggregate_measurement_supported){const metrics={...data.metrics};delete metrics.aggregate_latency;data={...data,metrics};}
 return {'/private/summary.json':JSON.stringify(data)};
}

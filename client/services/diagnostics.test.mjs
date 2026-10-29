import {test} from 'node:test';
import assert from 'node:assert/strict';
import {diagnosticTargets,isDiagnostic} from './diagnostics.ts';
import {eventOf} from './fixtures.mjs';
import {Results} from './results.ts';

function fixture(values, tap) {
  const record=eventOf(values);
  return {...record,dictionary:{...record.dictionary,tap}};
}
const resource='demo/payment/transactions.v1';
const sources=[`${resource}/debug`,`${resource}/trace`,'other/payment/transactions.v1/debug'];
const window={from:'2026-09-25T00:00:00Z',to:'2026-09-26T00:00:00Z'};
test('diagnostic leaves fall back to the exact timestamp and stay in their resource',()=>{
  const event=fixture({time:'2026-09-25T12:34:56.789Z',message:'saved'},`${resource}/write`);
  const targets=diagnosticTargets(event,sources,window);
  assert.equal(targets.length,2);
  for(const target of targets){
    assert.equal(target.linkedBy,'timestamp');
    assert.equal(target.query.from,'2026-09-25T12:34:56.789Z');
    assert.equal(target.query.to,'2026-09-25T12:34:56.790Z');
    assert.equal(target.query.include,'page');
    assert.deepEqual(target.query.query.filters,[]);
    assert.equal(target.source,`${resource}/${target.kind}`);
  }
  assert.deepEqual(diagnosticTargets(event,[]),[]);
  assert.equal(diagnosticTargets(fixture({correlation_id:null,trace_id:''},`${resource}/write`),sources)[0].linkedBy,'timestamp');
  assert.deepEqual(diagnosticTargets(fixture({},sources[0]),sources),[]);
});
test('developer correlation and trace IDs take precedence without losing wide integers',()=>{
  const event=fixture({time:'2026-09-25T12:00:00Z',correlation_id:18446744073709551615n,trace_id:'trace/42'},`${resource}/read`);
  const [debug,trace]=diagnosticTargets(event,sources,window);
  assert.equal(debug.linkedBy,'correlation_id');
  assert.deepEqual(debug.query.query.filters,[{field:'correlation_id',op:'==',value:{kind:'number',value:'18446744073709551615'}}]);
  assert.equal(trace.linkedBy,'trace_id');
  assert.equal(trace.query.query.filters[0].value.value,'trace/42');
  assert.equal(debug.query.from,window.from);assert.equal(debug.query.to,window.to);
  const [zero]=diagnosticTargets(fixture({correlation_id:0},`${resource}/write`),sources);
  assert.equal(zero.linkedBy,'correlation_id');assert.equal(zero.value,'0');
});
test('debug and trace never enter normal event counts, histograms, indicators or rankings',()=>{
  const operational=fixture({level:'INFO',duration:10,service:'payment'},`${resource}/write`);
  const diagnostics=[fixture({level:'ERROR',duration:999999,service:'payment'},`${resource}/debug`),fixture({level:'INFO',duration:999999,service:'payment'},`${resource}/trace`)];
  const results=new Results();
  assert.equal(results.append([operational,...diagnostics]),1);
  assert.deepEqual(results.events,[operational]);
  const expected=new Results();expected.append([operational]);
  assert.deepEqual(results.summary(),expected.summary());
  assert.equal(isDiagnostic(diagnostics[0]),true);
  assert.equal(isDiagnostic(eventOf({level:'DEBUG'})),true);
});

import {test} from 'node:test';
import assert from 'node:assert/strict';
import {parseQuery,completeQuery,EventQuery} from './query.ts';
const fields=[{name:'state',type:'STRING',values:['authorized','pending','on hold']},{name:'duration',type:'U64',values:[]},{name:'synthetic',type:'BOOL',values:[]}];
test('the first letter offers filtered root completions without definitions',()=>{
 assert.deepEqual(completeQuery('e',1,[]).map(c=>c.label),['events','events.query()']);
 assert.deepEqual(completeQuery('x',1,[]),[]);
 assert.deepEqual(completeQuery('events ',7,[]).map(c=>c.label),['where','search','order','page']);
 assert.deepEqual(completeQuery('events w',8,[]).map(c=>c.label),['where']);
});
test('selected completions advance from the first letter through multiple clauses',async()=>{
 const {guidedCompletion}=await import('./query.ts');
 let text='e';
 for(const label of ['events','where','state','==','authorized','order','timestamp','desc','page','100']){
  const choice=completeQuery(text,text.length,fields).find(c=>c.label===label);
  assert.ok(choice,`${label} after ${text}`);
  const c=guidedCompletion(text,choice);text=text.slice(0,c.from)+c.insert+text.slice(c.to);
  assert.ok(text.endsWith(' '));
 }
 const ast=parseQuery(text,fields);assert.equal(ast.page.size,100);assert.equal(ast.plan.order,'desc');
 assert.equal(completeQuery(text,text.length,fields).length,0);
 for(const prefix of ['events | where duration > 0 ', 'events.query().where(duration, >, 0) ']){
  assert.deepEqual(completeQuery(prefix+'s',prefix.length+1,fields).map(c=>c.label),['search']);
 }
});
test('pipeline, fluent text and builder produce the same typed AST',()=>{
 const pipeline='events query | where state == authorized | where duration >= 18446744073709551615 | search "timed out" | order timestamp desc | page 50';
 const fluent='events.query().where(state, ==, authorized).where(duration, >=, 18446744073709551615).search("timed out").order(timestamp, desc).page(50)';
 const builder=new EventQuery().where('state','==','authorized').where('duration','>=',18446744073709551615n).search('timed out').order('timestamp','desc').page(50).toAST();
 assert.deepEqual(parseQuery(pipeline,fields),builder);assert.deepEqual(parseQuery(fluent,fields),builder);
});
test('literals, shorthand and cursor are preserved without execution',()=>{
 assert.deepEqual(parseQuery('"a|b\\"c"',fields).plan.search,{text:'a|b"c',mode:'contains'});
 const ast=parseQuery('events query | where synthetic == false | where state != null | page 10 after "opaque.cursor-1"',fields);
 assert.deepEqual(ast.plan.filters.map(f=>f.value),[{kind:'boolean',value:'false'},{kind:'null',value:''}]);assert.equal(ast.page.after,'opaque.cursor-1');
 for(const query of ['events.delete()','events query | where unknown == 1','events query | search "unfinished','events query | page 0','events query | page 10 | search "later"','events.query(); fetch("bad")','events query | where state == unknown'])assert.throws(()=>parseQuery(query,fields));
 assert.throws(()=>new EventQuery().where('duration','==',Number.MAX_SAFE_INTEGER+1),/bigint/);
});
test('autocomplete handles incomplete syntax, field types and mapped labels',()=>{
 for(const query of ['events query | where dur','events.query().where(dur'])assert.equal(completeQuery(query,query.length,fields)[0].label,'duration');
 for(const query of ['events query | where state == au','events.query().where(state, ==, au'])assert.equal(completeQuery(query,query.length,fields)[0].insert,'authorized');
 const query='events query | where state == on';assert.equal(completeQuery(query,query.length,fields)[0].insert,'"on hold"');
 assert.ok(completeQuery('events query | pa',17,fields).some(c=>c.label==='page'));
 const mid='events query | where dur | page 50',at=mid.indexOf(' | page');const c=completeQuery(mid,at,fields)[0];assert.equal(mid.slice(0,c.from)+c.insert+mid.slice(c.to),'events query | where duration | page 50');
});

test('builder and pipeline formatting preserve exact values, cursors and predicates',async()=>{
 const {formatQuery}=await import('./query.ts');
 const ast=parseQuery('events query | where duration >= 18446744073709551615 | where state == "on hold" | search "timeout" | order timestamp desc | page 50 after "opaque"',fields);
 for(const syntax of ['builder','pipeline'])assert.deepEqual(parseQuery(formatQuery(ast,syntax),fields),ast);
});
test('guided completion advances arguments without changing following page clauses',async()=>{
 const {guidedCompletion}=await import('./query.ts');
 let text='events.query()\n  .where(';
 for(const label of ['state','==','authorized']){
  const choices=completeQuery(text,text.length,fields),choice=choices.find(c=>c.label===label);assert.ok(choice,label);
  const c=guidedCompletion(text,choice);text=text.slice(0,c.from)+c.insert+text.slice(c.to);
 }
 assert.equal(text,'events.query()\n  .where(state, ==, authorized) ');assert.equal(parseQuery(text,fields).plan.filters[0].value.value,'authorized');
 const before='events.query()\n  .where(st',tail='\n  .page(50)';
 const c=guidedCompletion(before+tail,completeQuery(before+tail,before.length,fields)[0]);
 assert.equal(c.insert,'state, ');assert.equal((before+tail).slice(c.to),tail);
});

test('level and access compose independently in both syntaxes',()=>{
 const fields=[{name:'level',type:'STRING',values:['INFO','ERROR','DEBUG','TRACE']},{name:'access',type:'STRING',values:['read','write']}];
 for(const level of fields[0].values)for(const access of fields[1].values){
  const a=parseQuery(`events.query().where(level, ==, ${level}).where(access, ==, ${access})`,fields);
  const b=parseQuery(`events query | where level == ${level} | where access == ${access}`,fields);
  assert.deepEqual(a,b);assert.equal(a.plan.filters.length,2);
 }
 assert.deepEqual(completeQuery('events.query().where(access, ==, ',31,fields).filter(c=>c.detail==='Mapped value').map(c=>c.label),['read','write']);
});

test('full-text modes share one AST and survive syntax switches',async()=>{
 const {formatQuery,searchModes}=await import('./query.ts');
 for(const mode of searchModes){
  const a=parseQuery(`events.query().search("connection timeout", ${mode}).page(50)`,fields);
  const b=parseQuery(`events query | search "connection timeout" mode ${mode} | page 50`,fields);
  assert.deepEqual(a,b);assert.deepEqual(a,new EventQuery().search('connection timeout',mode).page(50).toAST());
  for(const syntax of ['builder','pipeline'])assert.deepEqual(parseQuery(formatQuery(a,syntax),fields),a);
 }
 for(const text of ['events.query().search("timeout", ','events query | search "timeout" mode '])assert.deepEqual(completeQuery(text,text.length,fields).map(c=>c.label),searchModes);
 assert.throws(()=>parseQuery('events query | search "timeout" mode wildcard',fields));
});

test('pipeline root is sufficient and optional query has identical semantics',async()=>{
 const {formatQuery}=await import('./query.ts');
 for(const suffix of ['', ' | where duration > 0 | order timestamp desc | page 50']){
  const ast=parseQuery('events'+suffix,fields);
  assert.deepEqual(ast,parseQuery('events query'+suffix,fields));
  assert.ok(formatQuery(ast,'pipeline').startsWith('events'));
  assert.ok(!formatQuery(ast,'pipeline').includes('events query'));
 }
});
test('spaces advance root, field, operator and value completions without backspacing',async()=>{
 const {guidedCompletion}=await import('./query.ts');
 let text='events ';
 for(const label of ['where','state','==','authorized']){
  const choice=completeQuery(text,text.length,fields).find(c=>c.label===label);
  assert.ok(choice,`${label} after ${text}`);
  const c=guidedCompletion(text,choice);text=text.slice(0,c.from)+c.insert+text.slice(c.to);
  assert.ok(text.endsWith(' '));
 }
 assert.equal(text,'events | where state == authorized ');
 assert.equal(parseQuery(text,fields).plan.filters.length,1);
 for(const text of ['events ', 'events query ', 'events | ', 'events | where ', 'events | where duration ', 'events | where duration > '])assert.ok(completeQuery(text,text.length,fields).length,text);
});
test('numeric and order suggestions complete valid clauses and builder punctuation',async()=>{
 const {guidedCompletion}=await import('./query.ts');
 for(const start of ['events | where duration > ','events.query().where(duration, >, ']){
  const c=guidedCompletion(start,completeQuery(start,start.length,fields).find(c=>c.label==='0'));
  const text=start+c.insert;assert.ok(text.endsWith(' '));assert.equal(parseQuery(text,fields).plan.filters[0].value.value,'0');
 }
 for(let text of ['events | order ','events.query().order(']){
  for(const label of ['timestamp','desc']){const c=guidedCompletion(text,completeQuery(text,text.length,fields).find(c=>c.label===label));text+=c.insert;}
  assert.equal(parseQuery(text,fields).plan.order,'desc');
 }
});
test('invalid typed comparisons fail before execution and boolean menus omit ranges',()=>{
 for(const q of ['events | where duration contains "0"','events | where duration > null','events | where synthetic > false','events | where duration == "0"','events | where state == 1'])assert.throws(()=>parseQuery(q,fields),q);
 const q='events | where synthetic ';
 assert.deepEqual(completeQuery(q,q.length,fields).map(c=>c.label),['==','!=']);
 const range='events | where duration > ';assert.ok(!completeQuery(range,range.length,fields).some(c=>c.label==='null'));
});

test('duration-role queries default to milliseconds and preserve explicit units',async()=>{
 const {formatQuery,guidedCompletion}=await import('./query.ts');
 const typed=[{name:'elapsed',type:'U64',values:[],unit:'us'},{name:'count',type:'U64',values:[]}];
 for(const [input,value] of [['10000','10000ms'],['2s','2s'],['100us','100us'],['100 µs','100µs'],['100μs','100μs'],['1.5ms','1.5ms'],['500ns','500ns'],['1e3ms','1e3ms']]){
  const ast=parseQuery(`events | where elapsed <= ${input}`,typed);
  assert.deepEqual(ast.plan.filters[0].value,{kind:'duration',value});
  for(const syntax of ['pipeline','builder'])assert.deepEqual(parseQuery(formatQuery(ast,syntax),typed),ast);
 }
 assert.deepEqual(parseQuery('events | where count <= 10000',typed).plan.filters[0].value,{kind:'number',value:'10000'});
 assert.throws(()=>parseQuery('events | where count <= 10ms',typed),/duration field/);
 const input='events | where elapsed <= ';
 const choice=completeQuery(input,input.length,typed).find(c=>c.label==='0');
 assert.equal(choice.detail,'Milliseconds');
 const completion=guidedCompletion(input,choice);
 const next=input.slice(0,completion.from)+completion.insert;
 assert.ok(completeQuery(next,next.length,typed).some(c=>c.label==='where'));
});

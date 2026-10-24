import {test} from 'node:test';
import assert from 'node:assert/strict';
import {parseQuery,completeQuery,EventQuery} from './query.ts';
const fields=[{name:'state',type:'STRING',values:['authorized','pending','on hold']},{name:'duration',type:'U64',values:[]},{name:'synthetic',type:'BOOL',values:[]}];
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
 assert.equal(text,'events.query()\n  .where(state, ==, authorized)');assert.equal(parseQuery(text,fields).plan.filters[0].value.value,'authorized');
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

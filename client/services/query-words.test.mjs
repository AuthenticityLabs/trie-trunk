import {test} from 'node:test';
import assert from 'node:assert/strict';
import {prefixWords,textCompletionContext,valueCompletions} from './query-words.ts';
import {guidedCompletion,parseQuery} from './query.ts';
const fields=[{name:'state',type:'STRING',values:['authorized','on hold','on "pause"']},{name:'message',type:'STRING',values:[]},{name:'duration',type:'U64',values:[]}];
const words=['connect','connected','connection','connections','timeout'];
const apply=(text,c)=>text.slice(0,c.from)+c.insert+text.slice(c.to);
test('word lookup uses bounded prefix results and preserves typed casing',()=>{
 assert.deepEqual(prefixWords(words,'conn',2),['connect','connected']);assert.deepEqual(prefixWords(words,'CONN',1),['CONNECT']);
 assert.deepEqual(prefixWords(words,'Conn',1),['Connect']);assert.deepEqual(prefixWords(words,'x'),[]);
});
test('free text completion replaces a word without altering quotes, phrases or clauses',()=>{
 for(const text of ['events.query().search("failed conntion retry")','events query | search "failed conntion retry"','"failed conntion retry"']){
  const caret=text.indexOf('conn')+4,c=valueCompletions(text,caret,fields,words).find(c=>c.label==='connection');
  assert.ok(c);assert.equal(c.detail,'English word');assert.equal(apply(text,guidedCompletion(text,c)),text.replace('conntion','connection'));
 }
 assert.equal(valueCompletions('events query | page 50 after "conn',35,fields,words).length,0);
 assert.equal(textCompletionContext('events.query().where("conn',25,fields),undefined);
 assert.equal(textCompletionContext('events.query().where(duration, ==, "conn',38,fields),undefined);
});
test('definition values take priority and remain safely quoted',()=>{
 for(const text of ['events.query().where(state, ==, "on")','events query | where state == "on"']){
  const caret=text.indexOf('"on')+3,options=valueCompletions(text,caret,fields,words);
  assert.equal(options.length,2);assert.ok(options.every(c=>c.detail==='Definition value'));
  const result=apply(text,guidedCompletion(text,options[1]));assert.equal(parseQuery(result,fields).plan.filters[0].value.value,'on "pause"');
 }
 const text='events.query().where(state, ==, "auth';const c=valueCompletions(text,text.length,fields,words)[0];
 assert.equal(parseQuery(apply(text,guidedCompletion(text,c)),fields).plan.filters[0].value.value,'authorized');
});

test('selecting the final word completes the literal and advances beyond its closing delimiter',()=>{
 for(const text of ['events | search "conn"','events.query().search("conn")','events | where message == "conn']){
  const at=text.indexOf('conn')+4;
  const c=valueCompletions(text,at,fields,words).find(c=>c.label==='connection');
  const result=apply(text,guidedCompletion(text,c));
  assert.ok(result.endsWith(' '));assert.ok(parseQuery(result,fields));
 }
});

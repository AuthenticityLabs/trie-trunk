import {test} from 'node:test';
import assert from 'node:assert/strict';
import {placeQueryFlyover,visibleQueryAnchor,queryFlyoverScroll} from './query-flyover.ts';

test('partly clipped caret keeps the completion menu anchored to the visible editor',()=>{
 const view={left:0,top:150,width:390,height:280},editor={left:20,top:100,width:350,height:140};
 assert.deepEqual(visibleQueryAnchor({left:60,top:142,bottom:166},editor,view),{left:60,top:150,bottom:166});
 assert.deepEqual(visibleQueryAnchor({left:60,top:225,bottom:249},editor,view),{left:60,top:225,bottom:240});
});
test('missing caret bounds fall back to the editor but offscreen editors stay hidden',()=>{
 const view={left:0,top:150,width:390,height:280},editor={left:20,top:180,width:350,height:140};
 assert.deepEqual(visibleQueryAnchor({left:NaN,top:0,bottom:0},editor,view),{left:20,top:296,bottom:320});
 assert.equal(visibleQueryAnchor({left:20,top:10,bottom:34},{...editor,top:0,height:100},view),undefined);
});

test('caret flyover clamps its width on a narrow, panned viewport',()=>{
 const p=placeQueryFlyover({left:330,top:120,bottom:145},{left:20,top:50,width:300,height:400},230);
 assert.equal(p.left,28);assert.equal(p.width,284);assert.equal(p.top,151);
 assert.ok(p.top+p.height<=442);
});
test('keyboard-obstructed caret stays below even when the menu must hide',()=>{
 const p=placeQueryFlyover({left:80,top:270,bottom:294},{left:0,top:100,width:390,height:210},230);
 assert.equal(p.top,300);assert.equal(p.height,2);assert.ok(p.top>294);
});
test('long menus shrink below the caret instead of escaping the viewport',()=>{
 const p=placeQueryFlyover({left:30,top:60,bottom:85},{left:0,top:0,width:390,height:230},300);
 assert.equal(p.top,91);assert.equal(p.height,131);
});

test('mobile menus anchor below the whole input, including multiline queries',()=>{
 const view={left:0,top:120,width:390,height:330},editor={left:20,top:180,width:350,height:150};
 const anchor=visibleQueryAnchor({left:60,top:200,bottom:224},editor,view,true);
 assert.equal(anchor.bottom,330);
 const menu=placeQueryFlyover(anchor,view,230);
 assert.equal(menu.top,336);assert.equal(menu.height,106);
});
test('mobile scrolling makes room without moving the editor above the viewport',()=>{
 const view={left:0,top:120,width:390,height:330},editor={left:20,top:320,width:350,height:120};
 const shift=queryFlyoverScroll(editor,view,230);
 assert.equal(shift,192);assert.equal(editor.top-shift,view.top+8);
 const moved={...editor,top:editor.top-shift};
 const anchor=visibleQueryAnchor({left:0,top:0,bottom:0},moved,view,true);
 const menu=placeQueryFlyover(anchor,view,230);
 assert.equal(menu.top,254);assert.equal(menu.height,188);
 assert.equal(queryFlyoverScroll(moved,view,230),0);
});
test('menus never overlap their anchor at narrow keyboard viewport heights',()=>{
 for(const height of [180,240,320,844])for(const top of [0,100,250,500]){
  const anchor={left:40,top,bottom:top+29};
  const menu=placeQueryFlyover(anchor,{left:0,top:0,width:390,height},230);
  assert.ok(menu.top>=anchor.bottom+6);assert.ok(menu.height>=0);
 }
});

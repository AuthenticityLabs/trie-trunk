import { test } from 'node:test';
import assert from 'node:assert/strict';
import { seedDirectory, serviceDirectory, emptyDashboard, changeDashboard, readDashboard, dashboardPartition } from './dashboard-subscriptions.ts';

const payment = 'prod/payment';
const identity = 'prod/identity';
test('service directory groups all real resources and branches', () => {
 const sources = [`${payment}/transactions.v1/read`, `${payment}/transactions.v1/write`, `${payment}/refunds.v1/error`, `${identity}/permissions.v1/debug`, 'demo', 'invalid/path'];
 const directory = serviceDirectory(sources);
 assert.equal(directory.length, 2);
 assert.equal(directory.find(s => s.id === payment).resources.length, 2);
 assert.equal(seedDirectory(sources).find(r => r.resource === 'transactions.v1').sources.length, 2);
 assert.deepEqual(directory.map(s => s.service), ['identity', 'payment']);
});
test('new resources join an existing service subscription without changing saved intent', () => {
 const state = changeDashboard(emptyDashboard(), {kind:'subscribe', services:[payment]}, 123);
 const sources = [`${payment}/transactions.v1/read`, `${payment}/refunds.v1/write`];
 const covered = serviceDirectory(sources).filter(s => state.subscriptions.some(sub => sub.service === s.id));
 assert.deepEqual(covered.flatMap(s => s.resources.map(r => r.resource)), ['refunds.v1', 'transactions.v1']);
 assert.deepEqual(state.subscriptions, [{service:payment,created:123}]);
});
test('subscription changes deduplicate and preserve other saved preferences', () => {
 let state = changeDashboard(emptyDashboard(), {kind:'subscribe', services:[payment, identity, payment]}, 123);
 assert.equal(state.subscriptions.length, 2);
 state = changeDashboard(state, {kind:'select', service:payment, closeDrawer:true});
 assert.equal(state.drawerOpen, false);
 state = changeDashboard(state, {kind:'drawer', open:false});
 state = changeDashboard(state, {kind:'subscribe', services:[payment]}, 456);
 assert.equal(state.subscriptions.find(s => s.service === payment).created, 123);
 assert.equal(state.service, payment); assert.equal(state.drawerOpen, false);
 state = changeDashboard(state, {kind:'remove', service:identity});
 assert.equal(state.service, payment);
 state = changeDashboard(state, {kind:'remove', service:payment});
 assert.equal(state.service, ''); assert.equal(state.subscriptions.length, 0);
});
test('same-named services in different caches remain independently subscribed', () => {
 let state = changeDashboard(emptyDashboard(), {kind:'subscribe',services:[payment, 'test/payment']});
 state = changeDashboard(state, {kind:'select',service:payment});
 state = changeDashboard(state, {kind:'remove',service:payment});
 assert.equal(state.service, ''); assert.equal(state.subscriptions.length, 1);
 assert.equal(state.subscriptions[0].service, 'test/payment');
});
test('unavailable saved services are retained, malformed state is rejected', () => {
 const state = changeDashboard(emptyDashboard(), {kind:'subscribe',services:[payment]});
 assert.deepEqual(readDashboard(structuredClone(state)), state);
 assert.throws(() => changeDashboard(state, {kind:'subscribe',services:['../fake']}));
 assert.throws(() => changeDashboard(state, {kind:'subscribe',services:[`${payment}/transactions.v1`]}));
 assert.throws(() => readDashboard({...state, subscriptions:[...state.subscriptions,...state.subscriptions]}));
 assert.throws(() => readDashboard({...state, service:'missing'}));
 assert.deepEqual(readDashboard(undefined), emptyDashboard());
});
test('credentials partition persistence without storing bearer tokens in keys', async () => {
 assert.equal(await dashboardPartition(' token-a '), await dashboardPartition('token-a'));
 assert.notEqual(await dashboardPartition('token-a'), await dashboardPartition('token-b'));
 assert.match(await dashboardPartition(''), /^dashboard-services:[a-f0-9]{64}$/);
 assert.ok(!(await dashboardPartition('token-a')).includes('token-a'));
});
test('modal commits add/remove deltas atomically without losing another tab additions', () => {
 let current=changeDashboard(emptyDashboard(),{kind:'subscribe',services:[payment,identity]},100);
 current=changeDashboard(current,{kind:'subscribe',services:['prod/billing']},200);
 current=changeDashboard(current,{kind:'subscriptions',add:['prod/gateway'],remove:[identity]},300);
 assert.deepEqual(current.subscriptions.map(s=>s.service),[payment,'prod/billing','prod/gateway']);
 assert.equal(current.subscriptions[0].created,100);
});

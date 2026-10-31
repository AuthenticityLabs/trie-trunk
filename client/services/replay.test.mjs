import { test } from "node:test";
import assert from "node:assert/strict";
import {
  objectParameters, historyURL, observedState, observeObject, validateObject,
  reconcileObject, ObjectChangedDuringRefresh,
  objectIdentityBytes, validateCachedObject,
} from "./replay.ts";

const query = { source: "demo/payment/transactions.v1/error", id: "18446744073709551615" };
const token = "a".repeat(64), newer = "b".repeat(64);
const signal = () => new AbortController().signal;
const json = value => Response.json(value);

test("object identity stays lossless and escaped; lookup requires an exact collection", () => {
  assert.equal(objectParameters(query).get("id"), query.id);
  for (const id of ["customer/ID ?✓", "", "AP_-"]) {
    const url = new URL(historyURL({ ...query, id, from: "2026-10-16T00:00:00Z", to: "2026-10-17T00:00:00Z" }), "https://localhost");
    assert.equal(url.pathname, '/demo/collections');
    assert.equal(url.searchParams.get('id'), id);
    assert.equal(url.searchParams.get('branch'), 'payment/transactions.v1/write');
    assert.equal(url.searchParams.has("collection"), false);
  }
  for (const source of ["demo", "*/payment/transactions.v1/write", "demo/payment/*/write"])
    assert.throws(() => objectParameters({ ...query, source }));
  assert.throws(() => historyURL({ ...query, from: "bad", to: "bad" }));
  assert.deepEqual(observedState(query, token), { ...query, source: "demo/payment/transactions.v1/write", token });
});
test("local object identity encoding matches declared wire types without number rounding",()=>{
 assert.deepEqual([...objectIdentityBytes("U64","18446744073709551615")],Array(8).fill(255));
 assert.deepEqual([...objectIdentityBytes("U16","256")],[0,1]);
 assert.deepEqual([...objectIdentityBytes("BYTES","AP_-")],[0,255,254]);
 assert.equal(objectIdentityBytes("UUID","00112233-4455-6677-8899-aabbccddeeff").length,16);
 assert.equal(new TextDecoder().decode(objectIdentityBytes("STRING","customer/✓")),"customer/✓");
 for(const [kind,id] of [["U8","256"],["U64","-1"],["UUID","bad"],["BYTES","a="],["STRING","\ud800"]])assert.throws(()=>objectIdentityBytes(kind,id));
});
test("cached reconciliation compares the consumer token and treats unavailable state as unknown",async()=>{
 const state=observedState(query,token);
 assert.equal(await validateCachedObject(state,"auth",async(credential,c)=>{
  assert.equal(credential,"auth");assert.equal(c.kind,"native-object-state");
  return {nativeObjectState:{token,sequence:1}};
 }),true);
 assert.equal(await validateCachedObject(state,"auth",async()=>({nativeObjectState:{token:newer,sequence:2}})),false);
 await assert.rejects(validateCachedObject(state,"auth",async()=>({})),/No committed/);
});

test("origin reconciliation validates only the caller's observed invalidation token", async () => {
  let current = token;
  const calls = [];
  const fetcher = async (url, options) => {
    calls.push(url);
    assert.equal(options.cache, "no-store");
    assert.equal(options.headers.Authorization, "Bearer secret");
    const before = new URL(url, "https://localhost").searchParams.get("token");
    return json({ token: current, ...(before === null ? {} : { current: before === current }) });
  };
  const original = await observeObject(query, " secret ", signal(), fetcher);
  const refreshed = await reconcileObject(query, "secret", signal(), async () => ({ state: "authorized" }), fetcher);
  assert.deepEqual(refreshed.value, { state: "authorized" });
  assert.equal(refreshed.state.token, token);
  current = newer;
  assert.equal(await validateObject(original, "secret", signal(), fetcher), false);
  const second = await reconcileObject(query, "secret", signal(), async () => null, fetcher);
  assert.equal(second.value, null); // Authoritative deletion is also a full refresh.
  assert.equal(second.state.token, newer);
  assert.equal(await validateObject(original, "secret", signal(), fetcher), false);
  assert.ok(calls.every(url => url.startsWith("/demo/collections?")));
});

test("an error during origin fetch prevents publishing the fetched value as validated", async () => {
  let current = token;
  const fetcher = async url => {
    const before = new URL(url, "https://localhost").searchParams.get("token");
    return json({ token: current, ...(before === null ? {} : { current: before === current }) });
  };
  await assert.rejects(reconcileObject(query, "", signal(), async () => {
    current = newer;
    return { state: "possibly-stale" };
  }, fetcher), ObjectChangedDuringRefresh);
});

test("offline, malformed, inconsistent, and cancelled validation never become a cache hit", async () => {
  const state = observedState(query, token);
  for (const fetcher of [
    async () => { throw Error("offline"); },
    async () => json({ token: newer, current: true }),
    async () => json({ token, current: "true" }),
    async () => json({ token: "invalid", current: true }),
    async () => new Response("denied", { status: 403 }),
    async () => new Response(JSON.stringify({ token, current: true })),
  ]) await assert.rejects(validateObject(state, "", signal(), fetcher));
  const controller = new AbortController(); controller.abort();
  await assert.rejects(observeObject(query, "", controller.signal, async () => {
    assert.fail("aborted lookup fetched");
  }), { name: "AbortError" });
});

import assert from "node:assert/strict";
import { test } from "node:test";
import { matchesSource, sourceScope, scopeSource, sourceOptions, validSource, resourceScope } from "./source.ts";
import { compileSearch } from "./search.ts";
import { eventOf } from "./fixtures.mjs";

const catalog = ["demo/payment/transactions.v1/read", "demo/payment/transactions.v1/write",
  "demo/customer/accounts.v2/error", "prod/payment/transactions.v1/error", "legacy"];
test("catalog selectors narrow complete components without becoming stream identities", () => {
  assert.equal(scopeSource({}, catalog), "");
  assert.equal(scopeSource({cache:"demo",service:"payment"},catalog), "demo/payment/*/*");
  assert.equal(scopeSource({resource:"transactions.v1"},catalog), "*/*/transactions.v1/*");
  assert.equal(scopeSource({cache:"legacy"},catalog), "legacy");
  assert.deepEqual(sourceScope("demo/*/*/write"), {cache:"demo",service:"",resource:""});
  assert.deepEqual(catalog.filter(s=>matchesSource("demo/*/*/error",s)), [catalog[2]]);
  assert.equal(matchesSource("dem*/*/*/*",catalog[0]),false);
  assert.equal(matchesSource("demo/*",catalog[0]),false);
  assert.equal(matchesSource("*/*/*/*","legacy"),false);
  assert.equal(validSource("demo/*/*/write"),false);
  assert.deepEqual(sourceOptions(catalog,{cache:"demo",service:"payment"},2), ["transactions.v1"]);
  assert.equal(scopeSource(sourceScope("demo/payment/transactions.v1/error"),catalog),"demo/payment/transactions.v1/*");
  assert.equal(resourceScope("demo/payment/transactions.v1/error"),"demo/payment/transactions.v1/*");
  assert.equal(resourceScope(""),"");
});
test("object attributes are local criteria without assuming logging fields", () => {
  const predicate=compileSearch({parameters:{state:"authorized"},tap:"",from:"",to:""});
  assert.equal(predicate(eventOf({state:"authorized"})),true);
  assert.equal(predicate(eventOf({state:"pending"})),false);
  assert.equal(predicate(eventOf({})),false);
});

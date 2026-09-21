import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { RPCServer } from "../src/rpc-server.mjs";
import { BrowserBridge } from "../src/bridge.mjs";

function fakeBridge() { return { dispatch: async (method, params, session) => { if (method === "health") return { ok: true }; if (method === "session.open") return { session }; if (method === "echo") return params; throw Object.assign(new Error("bad method"), { code: "METHOD_NOT_FOUND" }); } }; }

test("RPC requires bearer token and returns structured errors", async () => {
  const server = new RPCServer({ pipeName: "test", token: "secret", bridge: fakeBridge() });
  const request = (headers, body) => server.handle({ method: "POST", url: "/rpc", headers, async *[Symbol.asyncIterator]() { yield Buffer.from(JSON.stringify(body)); } });
  assert.equal((await request({}, { method: "health" })).status, 401);
  assert.deepEqual((await request({ authorization: "Bearer secret" }, { method: "echo", params: { ok: 1 } })).body.result, { ok: 1 });
  await assert.rejects(() => request({ authorization: "Bearer secret" }, { method: "bad" }), error => error.code === "METHOD_NOT_FOUND");
});

test("BrowserBridge session and lease lifecycle", async () => {
  const driver = { context: {}, start: async () => {}, isHealthy: () => true, listTabs: async () => [], close: async () => {} };
  const root = await fsDir();
  const config = { stateDir: root, stateFile: path.join(root, "state.json"), backupFile: path.join(root, "backup.json"), leaseTTLms: 600000 };
  const bridge = new BrowserBridge({ config, driver }); await bridge.start();
  const s1 = (await bridge.dispatch("session.open", {}, "")).sessionId; await bridge.dispatch("taskSpace.use", { ref: "one" }, s1);
  const s2 = (await bridge.dispatch("session.open", {}, "")).sessionId; await assert.rejects(() => bridge.dispatch("taskSpace.claim", { ref: "one" }, s2), error => error.code === "TASK_SPACE_BUSY" && error.message.includes("user has taken control"));
  await bridge.dispatch("session.close", {}, s1); await bridge.stop();
});

test("session capabilities are daemon-issued and stale leases can be reclaimed", async () => {
  let now = 1000;
  const root = await fsDir(); const config = { stateDir: root, stateFile: path.join(root, "state.json"), backupFile: path.join(root, "backup.json"), leaseTTLms: 100 };
  const driver = { context: {}, start: async () => {}, isHealthy: () => true, restorePages: async () => {}, close: async () => {} };
  const bridge = new BrowserBridge({ config, driver, now: () => now }); await bridge.start();
  await assert.rejects(() => bridge.dispatch("session.open", {}, "forged"), error => error.code === "SESSION_ID_FORBIDDEN");
  const first = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const second = (await bridge.dispatch("session.open", {}, "")).sessionId;
  assert.notEqual(first, second);
  await assert.rejects(() => bridge.dispatch("session.open", {}, first), error => error.code === "SESSION_ID_FORBIDDEN");
  await bridge.dispatch("taskSpace.use", { ref: "ttl-space" }, first);
  now += 50;
  await bridge.dispatch("taskSpace.use", { ref: "other-space" }, second);
  now += 60;
  await bridge.dispatch("taskSpace.claim", { ref: "ttl-space" }, second);
  await assert.rejects(() => bridge.dispatch("page.info", {}, first), error => error.code === "SESSION_NOT_FOUND" || error.code === "TASK_SPACE_LEASE_REQUIRED");
  await bridge.stop();
});

test("Bridge forwards each session's task space to every page operation", async () => {
  const root = await fsDir();
  const config = { stateDir: root, stateFile: path.join(root, "state.json"), backupFile: path.join(root, "backup.json") };
  const spaces = new Map(); const calls = [];
  const driver = {
    context: {}, start: async () => {}, isHealthy: () => true, restorePages: async id => { spaces.set(id, spaces.get(id) || { tabs: [], active: null }); },
    openOrReuseTab: async (id, url) => { calls.push(["open", id]); const state = spaces.get(id); const targetId = `tab-${id}`; state.tabs = [{ targetId, url }]; state.active = targetId; return { targetId, url }; },
    listTabs: async id => { calls.push(["list", id]); return spaces.get(id)?.tabs || []; },
    activePageId: id => spaces.get(id)?.active,
    js: async id => { calls.push(["js", id]); return `space-${id}`; },
    click: async id => { calls.push(["click", id]); return { ok: true }; },
    publishOnce: async (id, label, authorized) => { calls.push(["publish", id, label, authorized]); return { ok: true, finalPublishClicked: true }; },
    close: async () => {},
  };
  const bridge = new BrowserBridge({ config, driver }); await bridge.start();
  const [openedA, openedB] = await Promise.all([bridge.dispatch("session.open", {}, ""), bridge.dispatch("session.open", {}, "")]); const a = openedA.sessionId; const b = openedB.sessionId;
  await Promise.all([bridge.dispatch("taskSpace.use", { ref: "alpha" }, a), bridge.dispatch("taskSpace.use", { ref: "beta" }, b)]);
  await Promise.all([bridge.dispatch("tab.open", { url: "data:text/plain,alpha" }, a), bridge.dispatch("tab.open", { url: "data:text/plain,beta" }, b)]);
  const [alpha, beta] = await Promise.all([bridge.dispatch("page.js", { source: "() => location.href" }, a), bridge.dispatch("page.js", { source: "() => location.href" }, b)]);
  await Promise.all([bridge.dispatch("page.click", { target: [1, 1] }, a), bridge.dispatch("page.click", { target: [2, 2] }, b)]);
  await bridge.dispatch("page.publish", { expectedLabel: "发布", authorized: true }, a);
  assert.equal(alpha, "space-1"); assert.equal(beta, "space-2");
  assert.deepEqual((await bridge.dispatch("tabs.list", {}, a)).map(tab => tab.url), ["data:text/plain,alpha"]);
  assert.deepEqual((await bridge.dispatch("tabs.list", {}, b)).map(tab => tab.url), ["data:text/plain,beta"]);
  assert.ok(calls.some(([method, id]) => method === "click" && id === 1)); assert.ok(calls.some(([method, id]) => method === "click" && id === 2));
  assert.ok(calls.some(([method, id, label, authorized]) => method === "publish" && id === 1 && label === "发布" && authorized === true));
  await bridge.stop();
});

async function fsDir() { return (await import("node:fs/promises")).mkdtemp(path.join(os.tmpdir(), "bridge-rpc-state-")); }

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { BrowserBridge } from "../src/bridge.mjs";

async function makeBridge(driver, { now = () => Date.now(), leaseTTLms = 600000, operationDrainTimeoutMs = 15000 } = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-close-persist-"));
  const config = {
    stateDir: root,
    stateFile: path.join(root, "state.json"),
    backupFile: path.join(root, "backup.json"),
    leaseTTLms,
    operationDrainTimeoutMs,
  };
  const bridge = new BrowserBridge({ config, driver, now });
  await bridge.start();
  return { bridge, root };
}

test("session close persists the latest tabs and active URL before releasing the lease", async () => {
  const live = {
    tabs: [{ targetId: "live-page", url: "https://creator.example/upload" }],
    activePageId: "live-page",
  };
  const driver = {
    context: {},
    start: async () => {},
    isHealthy: () => true,
    restorePages: async () => {},
    listTabs: async () => live.tabs,
    activePageId: () => live.activePageId,
    close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver);
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "upload-task" }, sessionId);

  live.tabs = [{ targetId: "live-page", url: "https://creator.example/editor/uploaded-draft" }];
  const result = await bridge.dispatch("session.close", {}, sessionId);
  const persisted = await bridge.store.find(taskSpace.id);

  assert.equal(result.persisted, true);
  assert.equal(persisted.lastURL, "https://creator.example/editor/uploaded-draft");
  assert.equal(persisted.activePageId, "live-page");
  assert.deepEqual(persisted.pages, [{ logicalId: "live-page", lastURL: "https://creator.example/editor/uploaded-draft" }]);
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  assert.equal(bridge.sessions.has(sessionId), false);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("session close releases session and lease when page persistence fails", async () => {
  const driver = {
    context: {},
    start: async () => {},
    isHealthy: () => true,
    restorePages: async () => {},
    listTabs: async () => { throw new Error("page channel unavailable"); },
    close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver);
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "failed-persist-task" }, sessionId);

  const result = await bridge.dispatch("session.close", {}, sessionId);

  assert.equal(result.ok, false);
  assert.equal(result.persisted, false);
  assert.match(result.persistenceError, /page channel unavailable/);
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  assert.equal(bridge.sessions.has(sessionId), false);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("session close rejects new RPC and holds the lease until in-flight work settles", async () => {
  let finishPage;
  let pageStarted;
  const pageReady = new Promise(resolve => { pageStarted = resolve; });
  const pageFinished = new Promise(resolve => { finishPage = resolve; });
  const live = { tabs: [{ targetId: "live-page", url: "https://creator.example/editor" }], activePageId: "live-page" };
  const driver = {
    context: {},
    start: async () => {},
    isHealthy: () => true,
    restorePages: async () => {},
    listTabs: async () => live.tabs,
    activePageId: () => live.activePageId,
    js: async () => { pageStarted(); await pageFinished; return "done"; },
    close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver);
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "concurrent-close" }, sessionId);
  const pageOperation = bridge.dispatch("page.js", { source: "() => true" }, sessionId);
  await pageReady;

  const closing = bridge.dispatch("session.close", {}, sessionId);
  while (!bridge.sessions.get(sessionId)?.closing) await Promise.resolve();
  await assert.rejects(() => bridge.dispatch("tabs.list", {}, sessionId), error => error.code === "SESSION_CLOSING");
  assert.equal(bridge.leases.owner(taskSpace.id), sessionId);

  finishPage();
  assert.equal(await pageOperation, "done");
  assert.equal((await closing).persisted, true);
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("TTL reaping persists the active task space before releasing its lease", async () => {
  let now = 1000;
  const live = { tabs: [{ targetId: "ttl-page", url: "https://creator.example/upload" }], activePageId: "ttl-page" };
  const driver = {
    context: {}, start: async () => {}, isHealthy: () => true, restorePages: async () => {},
    listTabs: async () => live.tabs, activePageId: () => live.activePageId, close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver, { now: () => now, leaseTTLms: 100 });
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "ttl-persist" }, sessionId);
  live.tabs[0].url = "https://creator.example/editor/ttl-draft";
  now += 101;

  const results = await bridge.reapExpired(now);
  const persisted = await bridge.store.find(taskSpace.id);
  assert.equal(results[0].reason, "ttl");
  assert.equal(persisted.lastURL, "https://creator.example/editor/ttl-draft");
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  assert.equal(bridge.sessions.has(sessionId), false);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("bridge stop persists active task spaces before closing the browser", async () => {
  const events = [];
  const live = { tabs: [{ targetId: "stop-page", url: "https://creator.example/editor/stop-draft" }], activePageId: "stop-page" };
  const driver = {
    context: {}, start: async () => {}, isHealthy: () => true, restorePages: async () => {},
    listTabs: async () => { events.push("persist"); return live.tabs; },
    activePageId: () => live.activePageId,
    close: async () => {
      events.push("driver.close");
      live.tabs = [];
      live.activePageId = null;
    },
  };
  const { bridge, root } = await makeBridge(driver);
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "stop-persist" }, sessionId);

  const result = await bridge.stop();
  const persisted = await bridge.store.find(taskSpace.id);
  assert.equal(result.ok, true);
  assert.equal(persisted.lastURL, "https://creator.example/editor/stop-draft");
  assert.equal(persisted.activePageId, "stop-page");
  assert.deepEqual(persisted.pages, [{ logicalId: "stop-page", lastURL: "https://creator.example/editor/stop-draft" }]);
  assert.deepEqual(events, ["persist", "driver.close"]);
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  await fs.rm(root, { recursive: true, force: true });
});

test("TTL cleanup quarantines a task space until the stuck operation settles", async () => {
  let now = 1000;
  let pageStarted;
  const pageReady = new Promise(resolve => { pageStarted = resolve; });
  const never = new Promise(() => {});
  const live = { tabs: [{ targetId: "stuck-page", url: "https://creator.example/editor/stuck" }], activePageId: "stuck-page" };
  const driver = {
    context: {}, start: async () => {}, isHealthy: () => true, restorePages: async () => {},
    listTabs: async () => live.tabs, activePageId: () => live.activePageId,
    js: async () => { pageStarted(); return never; }, close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver, { now: () => now, leaseTTLms: 100, operationDrainTimeoutMs: 20 });
  const firstSession = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "stuck-ttl" }, firstSession);
  void bridge.dispatch("page.js", { source: "() => true" }, firstSession);
  await pageReady;
  now += 101;

  const reaped = await bridge.reapExpired(now);
  assert.equal(reaped[0].drainTimedOut, true);
  assert.equal(reaped[0].quarantined, true);
  assert.equal(bridge.leases.owner(taskSpace.id), firstSession);
  assert.equal(bridge.sessions.has(firstSession), true);

  const retrySession = (await bridge.dispatch("session.open", {}, "")).sessionId;
  await assert.rejects(
    () => bridge.dispatch("taskSpace.claim", { ref: taskSpace.id }, retrySession),
    error => error.code === "TASK_SPACE_BUSY",
  );
  await bridge.stop();
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  await fs.rm(root, { recursive: true, force: true });
});

test("a quarantined task space is released after the timed-out operation settles", async () => {
  let now = 1000;
  let pageStarted;
  let finishPage;
  const pageReady = new Promise(resolve => { pageStarted = resolve; });
  const pageFinished = new Promise(resolve => { finishPage = resolve; });
  const live = { tabs: [{ targetId: "recover-page", url: "https://creator.example/editor/recover" }], activePageId: "recover-page" };
  const driver = {
    context: {},
    start: async () => {},
    isHealthy: () => true,
    restorePages: async () => {},
    listTabs: async () => live.tabs,
    activePageId: () => live.activePageId,
    js: async () => { pageStarted(); await pageFinished; return "done"; },
    close: async () => {},
  };
  const { bridge, root } = await makeBridge(driver, { now: () => now, leaseTTLms: 100, operationDrainTimeoutMs: 20 });
  const firstSession = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "recover-ttl" }, firstSession);
  const pageOperation = bridge.dispatch("page.js", { source: "() => true" }, firstSession);
  await pageReady;
  now += 101;

  const reaped = await bridge.reapExpired(now);
  const cleanup = bridge.sessions.get(firstSession).cleanupPromise;
  assert.equal(reaped[0].quarantined, true);
  assert.equal(bridge.leases.owner(taskSpace.id), firstSession);

  finishPage();
  assert.equal(await pageOperation, "done");
  await cleanup;
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  assert.equal(bridge.sessions.has(firstSession), false);

  const retrySession = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const reclaimed = await bridge.dispatch("taskSpace.claim", { ref: taskSpace.id }, retrySession);
  assert.equal(reclaimed.id, taskSpace.id);
  await bridge.dispatch("session.close", {}, retrySession);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("bridge stop closes the shared driver after a bounded stuck-operation drain", async () => {
  let pageStarted;
  const pageReady = new Promise(resolve => { pageStarted = resolve; });
  const never = new Promise(() => {});
  let driverClosed = false;
  const driver = {
    context: {}, start: async () => {}, isHealthy: () => true, restorePages: async () => {},
    listTabs: async () => [{ targetId: "stop-stuck-page", url: "https://creator.example/editor/stuck" }],
    activePageId: () => "stop-stuck-page",
    js: async () => { pageStarted(); return never; },
    close: async () => { driverClosed = true; },
  };
  const { bridge, root } = await makeBridge(driver, { operationDrainTimeoutMs: 20 });
  const sessionId = (await bridge.dispatch("session.open", {}, "")).sessionId;
  const taskSpace = await bridge.dispatch("taskSpace.use", { ref: "stop-stuck" }, sessionId);
  void bridge.dispatch("page.js", { source: "() => true" }, sessionId);
  await pageReady;

  let timeout;
  const result = await Promise.race([
    bridge.stop(),
    new Promise((resolve, reject) => { timeout = setTimeout(() => reject(new Error("bridge stop did not finish")), 500); }),
  ]).finally(() => clearTimeout(timeout));
  assert.equal(driverClosed, true);
  assert.equal(result.ok, false);
  assert.equal(result.sessions.some(item => item.drainTimedOut), true);
  assert.equal(bridge.leases.owner(taskSpace.id), null);
  assert.equal(bridge.sessions.has(sessionId), false);
  await fs.rm(root, { recursive: true, force: true });
});

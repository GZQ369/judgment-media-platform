import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { BrowserBridge } from "../src/bridge.mjs";
import { PlaywrightBrowserDriver } from "../src/browser.mjs";
import { acquireStartupLock } from "../src/startup-lock.mjs";

async function tempConfig() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-lock-"));
  return { root, startupLockFile: path.join(root, "startup.lock"), startupLockStaleMs: 1000, stateDir: path.join(root, "state"), stateFile: path.join(root, "state.json"), backupFile: path.join(root, "backup.json"), leaseTTLms: 600000 };
}

test("startup lock allows one winner and releases ownership safely", async () => {
  const config = await tempConfig(); let now = 1000;
  const first = await acquireStartupLock(config, { now: () => now, isProcessAlive: () => true });
  assert.ok(first); assert.equal(await acquireStartupLock(config, { now: () => now, isProcessAlive: () => true }), null);
  await first.release(); assert.ok(await acquireStartupLock(config, { now: () => now, isProcessAlive: () => true }));
  now += 1; await fs.rm(config.startupLockFile, { force: true }); await fs.rm(config.root, { recursive: true, force: true });
});

test("dead or stale startup lock can be reclaimed", async () => {
  const config = await tempConfig(); await fs.mkdir(config.root, { recursive: true });
  await fs.writeFile(config.startupLockFile, JSON.stringify({ pid: 123, timestamp: 1 }));
  const lock = await acquireStartupLock(config, { now: () => 5000, isProcessAlive: () => false });
  assert.ok(lock); await lock.release(); await fs.rm(config.root, { recursive: true, force: true });
});

test("recent half-written startup lock is busy and is not deleted", async () => {
  const config = await tempConfig(); const timestamp = Date.now();
  await fs.mkdir(config.root, { recursive: true });
  await fs.writeFile(config.startupLockFile, '{"pid":', "utf8");
  await fs.utimes(config.startupLockFile, new Date(timestamp), new Date(timestamp));
  const lock = await acquireStartupLock(config, { now: () => timestamp, isProcessAlive: () => true, sleep: async () => {} });
  assert.equal(lock, null);
  assert.equal(await fs.readFile(config.startupLockFile, "utf8"), '{"pid":');
  await fs.rm(config.root, { recursive: true, force: true });
});

test("recent malformed startup lock is busy and is not deleted", async () => {
  const config = await tempConfig(); const timestamp = Date.now();
  await fs.mkdir(config.root, { recursive: true });
  await fs.writeFile(config.startupLockFile, "null", "utf8");
  await fs.utimes(config.startupLockFile, new Date(timestamp), new Date(timestamp));
  const lock = await acquireStartupLock(config, { now: () => timestamp, isProcessAlive: () => true, sleep: async () => {} });
  assert.equal(lock, null);
  assert.equal(await fs.readFile(config.startupLockFile, "utf8"), "null");
  await fs.rm(config.root, { recursive: true, force: true });
});

test("concurrent startup lock acquisition has one winner", async () => {
  const config = await tempConfig();
  const locks = await Promise.all([
    acquireStartupLock(config, { isProcessAlive: () => true }),
    acquireStartupLock(config, { isProcessAlive: () => true }),
  ]);
  assert.equal(locks.filter(Boolean).length, 1);
  await locks.find(Boolean)?.release();
  await fs.rm(config.root, { recursive: true, force: true });
});

test("health follows driver health and reports an unexpected browser close", async () => {
  const config = await tempConfig(); let healthy = true;
  const driver = { context: {}, isHealthy: () => healthy, start: async () => {}, close: async () => {} };
  const bridge = new BrowserBridge({ config, driver }); await bridge.start();
  assert.equal((await bridge.dispatch("health", {}, "")).ok, true);
  healthy = false; driver.unhealthyReason = "browser context closed";
  const result = await bridge.dispatch("health", {}, ""); assert.equal(result.ok, false); assert.equal(result.reason, "browser context closed");
  await bridge.stop(); await fs.rm(config.root, { recursive: true, force: true });
});

test("expected browser close does not report unhealthy, unexpected close does", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-driver-close-"));
  const events = new Map();
  const context = {
    pages: () => [],
    on: (event, callback) => events.set(event, callback),
    addInitScript: async () => {},
    close: async () => events.get("close")?.(),
  };
  const driver = new PlaywrightBrowserDriver({ executablePath: "fake-browser", profileDir: root }, {
    chromium: { launchPersistentContext: async () => context },
  });
  const reasons = [];
  driver.onUnhealthy = reason => reasons.push(reason);
  await driver.start();
  events.get("close")();
  assert.equal(driver.isHealthy(), false);
  assert.equal(driver.unhealthyReason, "browser context closed");
  assert.deepEqual(reasons, ["browser context closed"]);

  const secondContext = {
    pages: () => [],
    on: (event, callback) => events.set(`second:${event}`, callback),
    addInitScript: async () => {},
    close: async () => events.get("second:close")?.(),
  };
  const secondDriver = new PlaywrightBrowserDriver({ executablePath: "fake-browser", profileDir: root }, {
    chromium: { launchPersistentContext: async () => secondContext },
  });
  const secondReasons = [];
  secondDriver.onUnhealthy = reason => secondReasons.push(reason);
  await secondDriver.start();
  await secondDriver.close();
  assert.deepEqual(secondReasons, []);
  assert.equal(secondDriver.isHealthy(), false);
  await fs.rm(root, { recursive: true, force: true });
});

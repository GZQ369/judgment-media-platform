import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { BrowserBridge } from "../src/bridge.mjs";

async function makeBridge(restorePages) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-switch-"));
  const config = { stateDir: root, stateFile: path.join(root, "state.json"), backupFile: path.join(root, "backup.json"), leaseTTLms: 600000 };
  const driver = { context: {}, start: async () => {}, restorePages, close: async () => {} };
  const bridge = new BrowserBridge({ config, driver });
  await bridge.start();
  return { bridge, root };
}

async function openSession(bridge) {
  return (await bridge.dispatch("session.open", {}, "")).sessionId;
}

test("switch commits the new lease and releases the old lease", async () => {
  const { bridge, root } = await makeBridge(async () => {});
  const sessionId = await openSession(bridge);
  const first = await bridge.dispatch("taskSpace.use", { ref: "first" }, sessionId);
  const second = await bridge.dispatch("taskSpace.use", { ref: "second" }, sessionId);
  assert.equal(bridge.leases.owner(first.id), null);
  assert.equal(bridge.leases.owner(second.id), sessionId);
  assert.equal(bridge.active.get(sessionId).id, second.id);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("failed restore keeps the old selection and releases only the new lease", async () => {
  const { bridge, root } = await makeBridge(async id => {
    if (id === 2) throw new Error("restore failed");
  });
  const sessionId = await openSession(bridge);
  const first = await bridge.dispatch("taskSpace.use", { ref: "first" }, sessionId);
  await assert.rejects(() => bridge.dispatch("taskSpace.use", { ref: "second" }, sessionId), /restore failed/);
  assert.equal(bridge.active.get(sessionId).id, first.id);
  assert.equal(bridge.leases.owner(first.id), sessionId);
  assert.equal(bridge.leases.owner(2), null);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("lease identity mismatch does not release another session's lease", async () => {
  const { bridge, root } = await makeBridge(async id => {
    if (id === 2) {
      bridge.leases.release(2, sessionId);
      bridge.leases.acquire(2, otherSessionId);
    }
  });
  const sessionId = await openSession(bridge);
  const otherSessionId = await openSession(bridge);
  const first = await bridge.dispatch("taskSpace.use", { ref: "first" }, sessionId);
  await assert.rejects(() => bridge.dispatch("taskSpace.use", { ref: "second" }, sessionId), error => error.code === "TASK_SPACE_LEASE_LOST");
  assert.equal(bridge.active.get(sessionId).id, first.id);
  assert.equal(bridge.leases.owner(first.id), sessionId);
  assert.equal(bridge.leases.owner(2), otherSessionId);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

test("concurrent switches for one session restore and commit in order", async () => {
  let releaseSecond;
  let secondStarted;
  const secondReady = new Promise(resolve => { secondStarted = resolve; });
  const secondRelease = new Promise(resolve => { releaseSecond = resolve; });
  const { bridge, root } = await makeBridge(async id => {
    if (id === 2) {
      secondStarted();
      await secondRelease;
    }
  });
  const sessionId = await openSession(bridge);
  await bridge.dispatch("taskSpace.use", { ref: "first" }, sessionId);
  const second = bridge.dispatch("taskSpace.use", { ref: "second" }, sessionId);
  await secondReady;
  const third = bridge.dispatch("taskSpace.use", { ref: "third" }, sessionId);
  releaseSecond();
  const [secondResult, thirdResult] = await Promise.all([second, third]);
  assert.equal(secondResult.name, "second");
  assert.equal(thirdResult.name, "third");
  assert.equal(bridge.active.get(sessionId).id, thirdResult.id);
  assert.equal(bridge.leases.owner(2), null);
  assert.equal(bridge.leases.owner(thirdResult.id), sessionId);
  await bridge.stop();
  await fs.rm(root, { recursive: true, force: true });
});

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { PlaywrightBrowserDriver } from "../src/browser.mjs";

class FakePage {
  constructor(url = "about:blank") { this.currentURL = url; this.events = new Map(); this.clicked = 0; this.pressed = []; this.selectorLabels = ["child", "发布"]; this.pointLabels = ["child", "发布"]; this.focusedLabels = ["child", "发表"]; }
  on(event, callback) { this.events.set(event, callback); }
  addInitScript() { return Promise.resolve(); }
  evaluate(source, target) {
    if (typeof source === "function") return Promise.resolve(Array.isArray(target) ? this.pointLabels : this.selectorLabels);
    if (String(source).includes("document.title")) return Promise.resolve("");
    if (String(source).includes("location.href")) return Promise.resolve(this.currentURL);
    if (String(source).includes("document.activeElement")) return Promise.resolve(this.focusedLabels);
    return Promise.resolve({ ok: true });
  }
  url() { return this.currentURL; }
  title() { return Promise.resolve(this.currentURL); }
  viewportSize() { return { width: 1280, height: 720 }; }
  goto(url) { this.currentURL = url; return Promise.resolve(); }
  waitForTimeout() { return Promise.resolve(); }
  locator() { return { click: async () => { this.clicked++; }, hover: async () => {}, setInputFiles: async () => {} }; }
  get mouse() { return { click: async () => { this.clicked++; }, move: async () => {} }; }
  get keyboard() { return { press: async key => { this.pressed.push(key); } }; }
}

function makeDriver(config = { allowedMediaRoot: "" }) {
  const created = [];
  const sessions = [];
  const context = {
    pages: () => [],
    addInitScript: async () => {},
    on: () => {},
    newPage: async () => { const page = new FakePage(); created.push(page); return page; },
    newCDPSession: async () => { const session = { calls: [], send: async (method, params) => { session.calls.push({ method, params }); return method === "Runtime.evaluate" ? { result: { objectId: "smoke-object-1" } } : { ok: true }; }, detach: async () => { session.detached = true; } }; sessions.push(session); return session; },
    close: async () => {},
  };
  const driver = new PlaywrightBrowserDriver(config, {});
  driver.context = context;
  return { driver, created, sessions };
}

test("two task spaces keep tabs, active pages and operations isolated", async () => {
  const { driver, created } = makeDriver();
  const a = await driver.openOrReuseTab(1, "data:text/plain,space-a");
  const b = await driver.openOrReuseTab(2, "data:text/plain,space-b");
  assert.equal((await driver.listTabs(1))[0].url, "data:text/plain,space-a");
  assert.equal((await driver.listTabs(2))[0].url, "data:text/plain,space-b");
  assert.equal(await driver.js(1, "() => location.href"), "data:text/plain,space-a");
  assert.equal(await driver.js(2, "() => location.href"), "data:text/plain,space-b");
  await assert.rejects(() => driver.click(1, "#publish-child"), error => error.code === "FINAL_PUBLISH_BLOCKED");
  await assert.rejects(() => driver.click(1, [10, 10]), error => error.code === "FINAL_PUBLISH_BLOCKED");
  await assert.rejects(() => driver.pressKey(1, "Enter"), error => error.code === "FINAL_PUBLISH_BLOCKED");
  created[0].pointLabels = ["继续"]; created[0].selectorLabels = ["继续"]; created[0].focusedLabels = ["输入框"];
  created[1].pointLabels = ["继续"]; created[1].selectorLabels = ["继续"]; created[1].focusedLabels = ["输入框"];
  await Promise.all([driver.click(1, [10, 10]), driver.click(2, [20, 20])]);
  assert.equal(created[0].clicked, 1); assert.equal(created[1].clicked, 1);
  await driver.switchTab(1, a.targetId); await driver.switchTab(2, b.targetId);
  assert.equal(driver.activePageId(1), a.targetId); assert.equal(driver.activePageId(2), b.targetId);
  assert.equal(driver.queues.size, 0);
});

test("page operations are serialized and queue entries are removed", async () => {
  const { driver } = makeDriver(); await driver.openOrReuseTab(7, "data:text/plain,queue");
  await driver.serial(7, driver.activePageId(7), async () => "done");
  assert.equal(driver.queues.size, 0);
});

test("one cached CDP session preserves objectId across calls and detaches on close", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-cdp-"));
  const file = path.join(root, "中文 clip.mp4"); await fs.writeFile(file, "video");
  const { driver, sessions } = makeDriver({ allowedMediaRoot: root }); await driver.openOrReuseTab(4, "data:text/plain,cdp");
  const evaluated = await driver.cdp(4, "Runtime.evaluate", { expression: "document.querySelector('input[type=file]')", objectGroup: "test" });
  await driver.cdp(4, "DOM.setFileInputFiles", { objectId: evaluated.result.objectId, files: [file] });
  assert.equal(sessions.length, 1); assert.equal(sessions[0].calls[1].params.objectId, "smoke-object-1"); assert.equal(sessions[0].calls[1].params.files[0], await fs.realpath(file));
  await driver.close(); assert.equal(sessions[0].detached, true); await fs.rm(root, { recursive: true, force: true });
});

import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { PlaywrightBrowserDriver } from "../src/browser.mjs";

class FakePage {
  constructor(url = "about:blank") { this.currentURL = url; this.events = new Map(); this.clicked = 0; this.pressed = []; this.closed = false; this.selectorLabels = ["child", "发布"]; this.pointLabels = ["child", "发布"]; this.focusedLabels = ["child", "发表"]; this.guard = { armed: true }; this.publication = null; this.onPublish = null; }
  on(event, callback) { this.events.set(event, callback); }
  addInitScript() { return Promise.resolve(); }
  evaluate(source, target) {
    if (typeof source === "function") {
      if (String(source).includes("__JUDGMENT_MEDIA_FINAL_PUBLISH_GUARD__")) return Promise.resolve(this.guard);
      if (String(source).includes("data-ego-publication-status")) return Promise.resolve(this.publication || { url: this.currentURL });
      return Promise.resolve(Array.isArray(target) ? this.pointLabels : this.selectorLabels);
    }
    if (String(source).includes("document.title")) return Promise.resolve("");
    if (String(source).includes("location.href")) return Promise.resolve(this.currentURL);
    if (String(source).includes("document.activeElement")) return Promise.resolve(this.focusedLabels);
    return Promise.resolve({ ok: true });
  }
  url() { return this.currentURL; }
  title() { return Promise.resolve(this.currentURL); }
  viewportSize() { return { width: 1280, height: 720 }; }
  goto(url) { this.currentURL = url; return Promise.resolve(); }
  close() { this.closed = true; this.events.get("close")?.(); return Promise.resolve(); }
  waitForTimeout() { return Promise.resolve(); }
  locator() { return { click: async () => { this.clicked++; }, hover: async () => {}, setInputFiles: async () => {} }; }
  getByText() { return { last: () => ({ count: async () => 1, isVisible: async () => true, isEnabled: async () => true, click: async () => { this.clicked++; this.onPublish?.(); } }) }; }
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
  return { driver, created, sessions, context };
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

test("final publish requires explicit current-run authorization", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(3, "data:text/plain,publish");
  await assert.rejects(() => driver.publishOnce(3, "发布笔记"), error => error.code === "PUBLISH_AUTH_REQUIRED");
  await assert.rejects(() => driver.publishOnce(3, "继续", true), error => error.code === "INVALID_PUBLISH_LABEL");
  const result = await driver.publishOnce(3, "发布笔记", true);
  assert.equal(result.finalPublishClicked, true);
  assert.equal(result.status, "unknown");
  assert.equal(result.evidenceState, "unknown");
  assert.equal(result.externalID, "");
  assert.equal(created[0].clicked, 1);
});

test("final publish returns only platform-bound submission evidence", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(31, "https://creator.douyin.com/creator-micro/content/manage");
  created[0].onPublish = () => { created[0].publication = { url: created[0].currentURL, status: "发布成功", externalID: "work-31" }; };
  const result = await driver.publishOnce(31, "发布", true, "douyin");

  assert.deepEqual({
    platform: result.platform,
    taskSpaceId: result.taskSpaceId,
    status: result.status,
    externalID: result.externalID,
    evidenceSource: result.evidenceSource,
    evidenceState: result.evidenceState,
  }, {
    platform: "douyin",
    taskSpaceId: "31",
    status: "submitted",
    externalID: "work-31",
    evidenceSource: "platform-dom",
    evidenceState: "identified",
  });
  assert.equal(result.finalPublishClicked, true);
  assert.equal(result.submissionCorrelation.taskSpaceId, "31");
  assert.ok(result.submissionCorrelation.id);
  assert.equal(created[0].clicked, 1);
});

test("generic success text and creator URLs remain truthful unknown", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(32, "https://creator.douyin.com/creator-micro/content/manage");
  created[0].publication = { url: created[0].currentURL, status: "success", externalID: "work-32" };
  const result = await driver.publishOnce(32, "发布", true, "douyin");
  assert.equal(result.status, "unknown");
  assert.equal(result.externalID, "");
  assert.equal(result.evidenceSource, "unknown");
  assert.equal(created[0].clicked, 1);
});

test("status change cannot correlate a pre-existing work ID", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(36, "https://creator.douyin.com/creator-micro/content/manage");
  created[0].publication = { url: created[0].currentURL, status: "审核中", externalID: "old-work-36" };
  created[0].onPublish = () => {
    created[0].publication = { url: created[0].currentURL, status: "发布成功", externalID: "old-work-36" };
  };

  const published = await driver.publishOnce(36, "发布", true, "douyin");
  assert.equal(published.finalPublishClicked, true);
  assert.equal(published.status, "unknown");
  assert.equal(published.externalID, "");
  assert.equal(published.evidenceSource, "unknown");

  const inspected = await driver.inspectPublication(36, "douyin");
  assert.equal(inspected.status, "unknown");
  assert.equal(inspected.externalID, "");
  assert.equal(inspected.evidenceSource, "unknown");
  assert.equal(created[0].clicked, 1);
});

test("pre-existing detail URL remains unknown during reconcile", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(33, "https://www.douyin.com/video/9001");
  const inspected = await driver.inspectPublication(33, "douyin");
  assert.equal(inspected.status, "unknown");
  assert.equal(inspected.externalID, "");
  assert.equal(inspected.evidenceSource, "unknown");
  assert.equal(created[0].clicked, 0);
});

test("authorized publish establishes correlation for the resulting detail page", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(34, "https://creator.douyin.com/creator-micro/content/manage");
  created[0].onPublish = () => {
    created[0].currentURL = "https://www.douyin.com/video/9002";
    created[0].publication = { url: created[0].currentURL };
  };

  const published = await driver.publishOnce(34, "发布", true, "douyin");
  assert.equal(published.status, "published");
  assert.equal(published.externalID, "9002");
  assert.equal(published.evidenceSource, "platform-detail-url");
  assert.equal(published.submissionCorrelation.taskSpaceId, "34");

  const inspected = await driver.inspectPublication(34, "douyin");
  assert.equal(inspected.status, "published");
  assert.equal(inspected.externalID, "9002");
  assert.deepEqual(inspected.submissionCorrelation, published.submissionCorrelation);
  assert.equal(created[0].clicked, 1);
});

test("reconcile rejects an unrelated detail page after a successful publish", async () => {
  const { driver, created } = makeDriver();
  await driver.openOrReuseTab(35, "https://creator.douyin.com/creator-micro/content/manage");
  created[0].onPublish = () => {
    created[0].publication = { url: "https://creator.douyin.com/creator-micro/content/manage", status: "发布成功", externalID: "work-35" };
  };
  await driver.publishOnce(35, "发布", true, "douyin");
  created[0].currentURL = "https://www.douyin.com/video/unrelated-35";
  created[0].publication = { url: created[0].currentURL, status: "已发布" };

  const inspected = await driver.inspectPublication(35, "douyin");
  assert.equal(inspected.status, "unknown");
  assert.equal(inspected.externalID, "");
  assert.equal(inspected.evidenceSource, "unknown");
  assert.equal(created[0].clicked, 1);
});

test("listTabs puts the switched active tab first and preserves the other tab order", async () => {
  const { driver } = makeDriver();
  const upload = await driver.openOrReuseTab(13, "https://creator.example/upload-manager");
  const cleanUpload = await driver.openOrReuseTab(13, "https://creator.example/clean-upload");
  const draftEditor = await driver.openOrReuseTab(13, "https://creator.example/draft-editor");

  await driver.switchTab(13, cleanUpload.targetId);

  assert.deepEqual((await driver.listTabs(13)).map(tab => tab.targetId), [
    cleanUpload.targetId,
    upload.targetId,
    draftEditor.targetId,
  ]);
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

test("restorePages keeps a logical live page when its URL changed after upload", async () => {
  const { driver, created } = makeDriver();
  const opened = await driver.openOrReuseTab(9, "https://creator.example/upload");
  created[0].currentURL = "https://creator.example/editor/uploaded-draft";

  await driver.restorePages(9, [{ logicalId: opened.targetId, lastURL: "https://creator.example/upload" }], {
    activePageId: opened.targetId,
  });

  assert.equal(created.length, 1);
  assert.equal((await driver.listTabs(9))[0].targetId, opened.targetId);
  assert.equal((await driver.listTabs(9))[0].url, "https://creator.example/editor/uploaded-draft");
  assert.equal(driver.activePageId(9), opened.targetId);
});

test("restorePages uses persisted URLs only on the first restore without live pages", async () => {
  const { driver, created } = makeDriver();
  const records = [{ logicalId: "persisted-page", lastURL: "https://creator.example/upload" }];

  await driver.restorePages(10, records, { activePageId: "persisted-page" });
  assert.equal(created.length, 1);
  assert.equal(driver.activePageId(10), "persisted-page");

  created[0].currentURL = "https://creator.example/editor/live-draft";
  await driver.restorePages(10, records, { activePageId: "persisted-page" });
  assert.equal(created.length, 1);
  assert.equal((await driver.listTabs(10))[0].url, "https://creator.example/editor/live-draft");
});

test("restorePages retries safely after a cold-start navigation failure", async () => {
  const { driver, created, context } = makeDriver();
  let attempts = 0;
  context.newPage = async () => {
    const page = new FakePage();
    if (attempts++ === 0) page.goto = async () => { throw new Error("navigation failed"); };
    created.push(page);
    return page;
  };
  const records = [{ logicalId: "retry-page", lastURL: "https://creator.example/upload" }];

  await assert.rejects(() => driver.restorePages(11, records, { activePageId: "retry-page" }), /navigation failed/);
  assert.equal(created[0].closed, true);
  assert.deepEqual(await driver.listTabs(11), []);
  assert.equal(driver.restoredSpaces.has(11), false);

  await driver.restorePages(11, records, { activePageId: "retry-page" });
  assert.equal(created.length, 2);
  assert.equal(driver.activePageId(11), "retry-page");
  assert.equal(driver.restoredSpaces.has(11), true);
});

test("cold restore selects the persisted logical active id when tab URLs are identical", async () => {
  const { driver } = makeDriver();
  const records = [
    { logicalId: "same-url-a", lastURL: "https://creator.example/editor" },
    { logicalId: "same-url-b", lastURL: "https://creator.example/editor" },
  ];

  await driver.restorePages(12, records, { activePageId: "same-url-b" });

  assert.equal((await driver.listTabs(12)).length, 2);
  assert.equal(driver.activePageId(12), "same-url-b");
  assert.deepEqual((await driver.listTabs(12)).map(tab => tab.targetId), ["same-url-b", "same-url-a"]);
});

test("cold restore migrates an old state without activePageId using activeURL", async () => {
  const { driver } = makeDriver();
  const records = [
    { logicalId: "old-upload", lastURL: "https://creator.example/upload" },
    { logicalId: "old-editor", lastURL: "https://creator.example/editor" },
  ];

  await driver.restorePages(14, records, { activeURL: "https://creator.example/editor" });

  assert.equal(driver.activePageId(14), "old-editor");
  assert.deepEqual((await driver.listTabs(14)).map(tab => tab.targetId), ["old-editor", "old-upload"]);
});

test("old-state activeURL chooses the first matching tab deterministically", async () => {
  const { driver } = makeDriver();
  const records = [
    { logicalId: "same-a", lastURL: "https://creator.example/editor" },
    { logicalId: "same-b", lastURL: "https://creator.example/editor" },
  ];

  await driver.restorePages(15, records, { activeURL: "https://creator.example/editor" });

  assert.equal(driver.activePageId(15), "same-a");
  assert.deepEqual((await driver.listTabs(15)).map(tab => tab.targetId), ["same-a", "same-b"]);
});

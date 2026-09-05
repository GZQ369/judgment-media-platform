import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { getConfig } from "../src/config.mjs";
import { PlaywrightBrowserDriver } from "../src/browser.mjs";

if (process.platform !== "win32") {
  console.log("Windows Browser Draft Runtime smoke skipped: Windows is required");
  process.exit(0);
}

const root = await fs.mkdtemp(path.join(os.tmpdir(), "judgment-media-browser-smoke-"));
const config = getConfig({ WINDOWS_BROWSER_BRIDGE_HOME: root, WINDOWS_BROWSER_BRIDGE_PROFILE: path.join(root, "profile") });
const taskSpaceId = 1;
const videoPath = path.join(root, "中文 smoke clip.mp4");
let driver = null;
try {
  const playwright = await import("playwright-core");
  driver = new PlaywrightBrowserDriver(config, playwright);
  await fs.writeFile(videoPath, "not a real video; only a file input smoke payload", "utf8");
  await driver.start();
  const html = "<input id=file type=file><button id=ok>继续</button><button id=publish>发布</button>";
  const dataURL = `data:text/html;charset=utf-8,${encodeURIComponent(html)}`;
  await driver.openOrReuseTab(taskSpaceId, dataURL);
  assert.equal((await driver.js(taskSpaceId, "(() => document.title)()")), "");
  const runtime = await driver.cdp(taskSpaceId, "Runtime.evaluate", { expression: "document.querySelector('#file')", objectGroup: "windows-browser-smoke" });
  const objectId = runtime?.result?.objectId;
  assert.ok(objectId, "Runtime.evaluate must return an objectId");
  await driver.cdp(taskSpaceId, "DOM.setFileInputFiles", { objectId, files: [videoPath] });
  assert.equal(await driver.js(taskSpaceId, "(() => document.querySelector('#file').files[0]?.name)()"), "中文 smoke clip.mp4");
  assert.equal((await driver.js(taskSpaceId, "(() => document.querySelector('#publish')?.innerText)()")), "发布");
  await driver.click(taskSpaceId, "#ok");
  await assert.rejects(() => driver.click(taskSpaceId, "#publish"), error => error.code === "FINAL_PUBLISH_BLOCKED");
  await driver.js(taskSpaceId, "(() => { document.querySelector('#publish').focus(); return true; })()");
  await assert.rejects(() => driver.pressKey(taskSpaceId, "Enter"), error => error.code === "FINAL_PUBLISH_BLOCKED");
  console.log("Windows Browser Draft Runtime smoke passed: safe DOM action allowed, final action blocked");
} finally {
  await driver?.close();
  await fs.rm(root, { recursive: true, force: true });
}

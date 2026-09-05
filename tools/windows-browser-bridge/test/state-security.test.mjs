import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import vm from "node:vm";
import { TaskSpaceStore, LeaseManager } from "../src/state.mjs";
import { getConfig } from "../src/config.mjs";
import { ALLOWED_CDP_METHODS, assertAllowedCDP, guardSource, isFinalPublishLabel, validateUploadPath } from "../src/security.mjs";

async function config() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-state-"));
  return { root, stateDir: path.join(root, "state"), stateFile: path.join(root, "state", "task-spaces.json"), backupFile: path.join(root, "state", "task-spaces.backup.json"), allowedMediaRoot: root };
}

test("task spaces create by name and restore by numeric id", async () => {
  const cfg = await config(); const first = new TaskSpaceStore(cfg);
  const created = await first.useOrCreate("creator", "douyin");
  assert.equal(created.id, 1); assert.equal((await first.find(1)).name, "creator");
  const second = new TaskSpaceStore(cfg); assert.equal((await second.find(1)).name, "creator");
  await assert.rejects(() => second.find(99), error => error.message.includes("task space not found"));
});

test("concurrent same-name creation is one transaction and one id", async () => {
  const cfg = await config(); const store = new TaskSpaceStore(cfg);
  const created = await Promise.all(Array.from({ length: 12 }, () => store.useOrCreate("same-name")));
  assert.equal(new Set(created.map(item => item.id)).size, 1); assert.equal((await store.load()).taskSpaces.length, 1);
});

test("state recovery preserves corrupt primary and restores atomic backup", async () => {
  const cfg = await config(); const store = new TaskSpaceStore(cfg);
  await store.useOrCreate("stable");
  await fs.copyFile(cfg.stateFile, cfg.backupFile); await fs.writeFile(cfg.stateFile, "{broken", "utf8");
  const recovered = new TaskSpaceStore(cfg); assert.equal((await recovered.load()).taskSpaces[0].name, "stable");
  const files = await fs.readdir(cfg.stateDir); assert.ok(files.some(file => file.startsWith("task-spaces.json.corrupt-")));
});

test("leases reject concurrent use of the same task space", () => {
  const leases = new LeaseManager(); leases.acquire(1, "a");
  assert.throws(() => leases.acquire(1, "b"), error => error.code === "TASK_SPACE_BUSY");
  leases.releaseSession("a"); assert.doesNotThrow(() => leases.acquire(1, "b"));
});

test("CDP allowlist and final label policy", () => {
  assert.ok(ALLOWED_CDP_METHODS.has("Runtime.evaluate")); assert.doesNotThrow(() => assertAllowedCDP("Input.insertText"));
  assert.throws(() => assertAllowedCDP("Network.getAllCookies"), error => error.code === "CDP_METHOD_NOT_ALLOWED");
  assert.equal(isFinalPublishLabel("发布"), true); assert.equal(isFinalPublishLabel("发布视频"), false);
});

test("upload path accepts ordinary media and rejects protected or non-media paths", async () => {
  const cfg = await config(); const media = path.join(cfg.root, "clip.mp4"); const secret = path.join(cfg.root, "profile", "token.mp4");
  await fs.writeFile(media, "video"); await fs.mkdir(path.dirname(secret), { recursive: true }); await fs.writeFile(secret, "secret");
  assert.equal(await validateUploadPath(media, cfg), await fs.realpath(media));
  await assert.rejects(() => validateUploadPath(path.join(cfg.root, "notes.txt"), cfg), error => error.code === "UPLOAD_TYPE_NOT_ALLOWED");
  await assert.rejects(() => validateUploadPath(secret, cfg), error => error.code === "UPLOAD_PATH_FORBIDDEN");
});

test("upload validation returns canonical realpath and rejects symlink escapes when supported", async () => {
  const root = await config(); const mediaRoot = path.join(root.root, "media"); const outside = path.join(root.root, "outside");
  await fs.mkdir(mediaRoot); await fs.mkdir(outside); const target = path.join(outside, "real.mp4"); await fs.writeFile(target, "video");
  const link = path.join(mediaRoot, "linked.mp4");
  try { await fs.symlink(target, link); } catch (error) { if (["EPERM", "EACCES", "ENOSYS"].includes(error.code)) return; throw error; }
  await assert.rejects(() => validateUploadPath(link, { allowedMediaRoot: mediaRoot }), error => error.code === "UPLOAD_PATH_FORBIDDEN");
});

test("LOCALAPPDATA is preferred for the default runtime root", () => {
  const cfg = getConfig({ LOCALAPPDATA: "C:\\Users\\creator\\AppData\\Local" });
  assert.equal(cfg.root, "C:\\Users\\creator\\AppData\\Local\\JudgmentMedia\\browser-bridge");
});

test("guard source installs capture listeners for DOM click and submit", () => {
  const listeners = {};
  const document = { addEventListener: (type, fn, capture) => { listeners[type] = { fn, capture }; } };
  const window = {};
  class Element { matches() { return true; } getAttribute() { return null; } }
  vm.runInNewContext(guardSource(), { window, document, Element, Date });
  const button = new Element(); button.innerText = "发布"; button.textContent = "发布";
  for (const type of ["click", "submit"]) {
    let prevented = false; let stopped = false;
    listeners[type].fn({ target: button, submitter: button, composedPath: () => [button], preventDefault: () => { prevented = true; }, stopImmediatePropagation: () => { stopped = true; } });
    assert.equal(prevented, true); assert.equal(stopped, true);
  }
  assert.equal(listeners.click.capture, true); assert.equal(listeners.submit.capture, true);
});

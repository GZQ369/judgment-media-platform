import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { Writable } from "node:stream";
import test from "node:test";
import { runNodeJS } from "../src/cli.mjs";

test("nodejs runner preserves static imports, top-level await, cliLog and exit code", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-cli-"));
  const input = 'import fs from "node:fs"; await Promise.resolve(); cliLog("import-ok:" + Boolean(fs.readFileSync)); process.exitCode = 7;'; const output = []; const errors = [];
  const fakeConfig = { runsDir: path.join(root, "runs") };
  const closed = [];
  const openSessionFn = async () => ({ sessionId: `issued-${closed.length + 1}` });
  const closeSessionFn = async (config, token, sessionId) => { closed.push(sessionId); };
  const code = await runNodeJS({ stdin: readable(input), stdout: writable(output), stderr: writable(errors), config: fakeConfig, token: "unused", ensureDaemonFn: async () => {}, bootstrapPath: path.resolve("fixtures", "runner-bootstrap.mjs"), openSessionFn, closeSessionFn });
  await fs.rm(root, { recursive: true, force: true });
  assert.equal(code, 7); assert.match(Buffer.concat(output).toString(), /import-ok:true/); assert.equal(Buffer.concat(errors).toString(), ""); assert.equal(closed.length, 1);
  const failed = await runNodeJS({ stdin: readable('throw new Error("expected failure");'), stdout: writable([]), stderr: writable([]), config: { runsDir: path.join(os.tmpdir(), `bridge-cli-failed-${process.pid}`) }, token: "unused", ensureDaemonFn: async () => {}, bootstrapPath: path.resolve("fixtures", "runner-bootstrap.mjs"), openSessionFn, closeSessionFn });
  assert.equal(failed, 1); assert.equal(closed.length, 2); assert.notEqual(closed[0], closed[1]);
});

test("compiled launcher forwards ESM stdin when available", async t => {
  if (process.platform !== "win32") { t.skip("launcher integration is Windows-specific"); return; }
  const exe = path.resolve("bin", "ego-browser.exe");
  try { await fs.access(exe); } catch { t.skip("run install.ps1 first to build ego-browser.exe"); return; }
  const cli = path.resolve("fixtures", "launcher-fixture.mjs");
  const child = spawn(exe, ["nodejs"], { shell: false, env: { ...process.env, WINDOWS_BROWSER_BRIDGE_CLI: cli } });
  child.stdin.end('import os from "node:os"; await Promise.resolve(); cliLog("launcher-esm-ok:" + Boolean(os.platform));');
  const stdout = []; const stderr = []; child.stdout.on("data", b => stdout.push(b)); child.stderr.on("data", b => stderr.push(b));
  const code = await new Promise(resolve => child.on("close", resolve));
  assert.equal(code, 0); assert.match(Buffer.concat(stdout).toString(), /launcher-esm-ok:true/); assert.equal(Buffer.concat(stderr).toString(), "");
});

test("nodejs runner reports session-close persistence failures", async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "bridge-cli-close-failure-"));
  const errors = [];
  const code = await runNodeJS({
    stdin: readable('process.exitCode = 0;'),
    stdout: writable([]),
    stderr: writable(errors),
    config: { runsDir: path.join(root, "runs") },
    token: "unused",
    ensureDaemonFn: async () => {},
    bootstrapPath: path.resolve("fixtures", "runner-bootstrap.mjs"),
    openSessionFn: async () => ({ sessionId: "close-failure-session" }),
    closeSessionFn: async () => ({ ok: false, persisted: false, persistenceError: "state write failed" }),
  });
  await fs.rm(root, { recursive: true, force: true });

  assert.equal(code, 1);
  assert.match(Buffer.concat(errors).toString(), /session close failed: state write failed/);
});

function readable(value) { return { setEncoding() {}, on(event, cb) { if (event === "data") cb(value); if (event === "end") cb(); return this; } }; }
function writable(chunks) { return new Writable({ write(value, encoding, callback) { chunks.push(Buffer.from(value)); callback(); } }); }

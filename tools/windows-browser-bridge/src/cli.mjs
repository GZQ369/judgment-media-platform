import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { getConfig } from "./config.mjs";
import { getToken } from "./auth.mjs";
import { RPCClient } from "./client.mjs";
import { acquireStartupLock } from "./startup-lock.mjs";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const daemonEntry = path.join(here, "daemon.mjs");
const bootstrap = path.join(here, "bootstrap.mjs");

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

export async function health(config, token) {
  const result = await new RPCClient({ ...config, token }).request("health", {});
  if (!result?.ok) throw new Error(`browser bridge is unhealthy: ${result?.reason || "unknown reason"}`);
  return result;
}

export async function ensureDaemon(config, token, { healthFn = health, spawnFn = (command, args, options) => spawn(command, args, options), lockFn = acquireStartupLock } = {}) {
  const deadline = Date.now() + config.launchTimeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try { return await healthFn(config, token); } catch (error) { lastError = error; }
    const lock = await lockFn(config);
    if (lock) {
      try {
        const child = spawnFn(process.execPath, [daemonEntry], { detached: true, stdio: "ignore", windowsHide: true, env: { ...process.env, WINDOWS_BROWSER_BRIDGE_TOKEN: token } });
        child.unref?.();
        while (Date.now() < deadline) {
          try { return await healthFn(config, token); } catch (error) { lastError = error; await sleep(250); }
        }
      } finally {
        await lock.release();
      }
      break;
    }
    await sleep(250);
  }
  throw new Error(`browser bridge daemon did not become ready: ${lastError?.message || "unknown error"}`);
}

export async function runNodeJS({ stdin = process.stdin, stdout = process.stdout, stderr = process.stderr, config = getConfig(), token = null, ensureDaemonFn = ensureDaemon, bootstrapPath = bootstrap, openSessionFn = client => client.openSession(), closeSessionFn = (targetConfig, targetToken, sessionId) => new RPCClient({ ...targetConfig, token: targetToken, sessionId }).closeSession() } = {}) {
  const script = await new Promise((resolve, reject) => { let value = ""; stdin.setEncoding("utf8"); stdin.on("data", chunk => { value += chunk; }); stdin.on("end", () => resolve(value)); stdin.on("error", reject); });
  const authToken = token || await getToken(config);
  await ensureDaemonFn(config, authToken);
  await fs.mkdir(config.runsDir, { recursive: true });
  const sessionClient = new RPCClient({ ...config, token: authToken });
  let sessionId = null;
  let scriptFile = null;
  let exitCode = 1;
  let runError = null;
  let closeError = null;
  try {
    const opened = await openSessionFn(sessionClient);
    sessionId = opened?.sessionId;
    if (!sessionId) throw new Error("daemon did not issue a session capability");
    scriptFile = path.join(config.runsDir, `task-${process.pid}-${crypto.randomUUID()}.mjs`);
    await fs.writeFile(scriptFile, script, "utf8");
    const child = spawn(process.execPath, ["--import", pathToFileURL(bootstrapPath).href, scriptFile], { stdio: ["ignore", "pipe", "pipe"], windowsHide: true, env: { ...process.env, WINDOWS_BROWSER_BRIDGE_TOKEN: authToken, WINDOWS_BROWSER_BRIDGE_SESSION_ID: sessionId } });
    child.stdout.pipe(stdout, { end: false }); child.stderr.pipe(stderr, { end: false });
    exitCode = await new Promise(resolve => { child.on("error", error => { stderr.write(`${error.stack || error}\n`); resolve(1); }); child.on("close", value => resolve(value ?? 1)); });
  } catch (error) {
    runError = error;
  } finally {
    if (scriptFile) await fs.rm(scriptFile, { force: true });
    if (sessionId) {
      try {
        const closed = await closeSessionFn(config, authToken, sessionId);
        if (closed?.ok === false || closed?.persistenceError) {
          const reason = closed.persistenceError || (closed.drainTimedOut ? "in-flight operation drain timed out" : "unknown error");
          closeError = new Error(`session close failed: ${reason}`);
        }
      } catch (error) {
        closeError = error;
      }
    }
  }
  if (closeError) {
    stderr.write(`${closeError.stack || closeError}\n`);
  }
  if (runError) throw runError;
  if (closeError) return 1;
  return exitCode;
}

export async function main(argv = process.argv.slice(2)) {
  const config = getConfig();
  const token = await getToken(config);
  if (argv[0] === "nodejs") return runNodeJS({ config, token });
  const client = new RPCClient({ ...config, token });
  if (argv[0] === "status") { process.stdout.write(`${JSON.stringify(await client.request("status"))}\n`); return 0; }
  if (argv[0] === "stop") { process.stdout.write(`${JSON.stringify(await client.request("shutdown"))}\n`); return 0; }
  process.stderr.write("Usage: ego-browser nodejs | status | stop\n"); return 2;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main().then(code => { if (typeof code === "number") process.exitCode = code; }).catch(error => { process.stderr.write(`${error.stack || error}\n`); process.exitCode = 1; });

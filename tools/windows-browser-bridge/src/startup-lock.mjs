import fs from "node:fs/promises";
import { ensureDir } from "./state.mjs";

export async function acquireStartupLock(config, { now = () => Date.now(), isProcessAlive = pid => {
  try { process.kill(pid, 0); return true; } catch { return false; }
}, fsImpl = fs, sleep = ms => new Promise(resolve => setTimeout(resolve, ms)) } = {}) {
  await ensureDir(config.root);
  const record = { pid: process.pid, timestamp: now() };
  const staleMs = Number(config.startupLockStaleMs || 30000);
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      await fsImpl.writeFile(config.startupLockFile, `${JSON.stringify(record)}\n`, { encoding: "utf8", flag: "wx" });
      return { record, release: async () => {
        try {
          const current = JSON.parse(await fsImpl.readFile(config.startupLockFile, "utf8"));
          if (current.pid === record.pid && current.timestamp === record.timestamp) await fsImpl.rm(config.startupLockFile, { force: true });
        } catch {}
      } };
    } catch (error) {
      if (error.code !== "EEXIST") throw error;
      let stat;
      try { stat = await fsImpl.stat(config.startupLockFile); } catch (statError) {
        if (statError.code === "ENOENT") continue;
        return null;
      }
      let existing = null;
      let complete = true;
      try {
        const parsed = JSON.parse(await fsImpl.readFile(config.startupLockFile, "utf8"));
        const pid = Number(parsed?.pid);
        const timestamp = Number(parsed?.timestamp);
        if (parsed && Number.isInteger(pid) && pid > 0 && Number.isFinite(timestamp)) existing = { pid, timestamp };
        else complete = false;
      } catch { complete = false; }
      if (!complete) {
        if (now() - stat.mtimeMs < staleMs) {
          if (attempt < 2) await sleep(10);
          continue;
        }
        await fsImpl.rm(config.startupLockFile, { force: true });
        continue;
      }
      const timestamp = Number(existing?.timestamp);
      const pid = Number(existing?.pid);
      const stale = !Number.isFinite(timestamp) || !Number.isInteger(pid) || now() - timestamp > staleMs || !isProcessAlive(pid);
      if (!stale) return null;
      await fsImpl.rm(config.startupLockFile, { force: true });
    }
  }
  return null;
}

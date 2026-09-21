import os from "node:os";
import path from "node:path";

const fallbackRoot = path.join(os.homedir(), "AppData", "Local", "JudgmentMedia", "browser-bridge");

export function getConfig(env = process.env) {
  const root = env.WINDOWS_BROWSER_BRIDGE_HOME || (env.LOCALAPPDATA ? path.join(env.LOCALAPPDATA, "JudgmentMedia", "browser-bridge") : fallbackRoot);
  const username = env.USERNAME || env.USER || "user";
  const safeUser = username.replace(/[^a-zA-Z0-9_.-]/g, "_");
  return {
    root,
    profileDir: env.WINDOWS_BROWSER_BRIDGE_PROFILE || path.join(root, "profile"),
    stateDir: path.join(root, "state"),
    stateFile: path.join(root, "state", "task-spaces.json"),
    backupFile: path.join(root, "state", "task-spaces.backup.json"),
    tokenFile: path.join(root, "auth.token"),
    runsDir: path.join(root, "runs"),
    logsDir: path.join(root, "logs"),
    pipeName: env.WINDOWS_BROWSER_BRIDGE_PIPE || `\\\\.\\pipe\\judgment-media-browser-bridge-${safeUser}`,
    executablePath: env.WINDOWS_BROWSER_BRIDGE_EXECUTABLE || "",
    allowedMediaRoot: env.WINDOWS_BROWSER_BRIDGE_MEDIA_ROOT || "",
    requestTimeoutMs: Number(env.WINDOWS_BROWSER_BRIDGE_REQUEST_TIMEOUT_MS || 120000),
    operationDrainTimeoutMs: Number(env.WINDOWS_BROWSER_BRIDGE_OPERATION_DRAIN_TIMEOUT_MS || 15000),
    launchTimeoutMs: Number(env.WINDOWS_BROWSER_BRIDGE_LAUNCH_TIMEOUT_MS || 30000),
    leaseTTLms: Number(env.WINDOWS_BROWSER_BRIDGE_LEASE_TTL_MS || 600000),
    startupLockStaleMs: Number(env.WINDOWS_BROWSER_BRIDGE_STARTUP_LOCK_STALE_MS || 30000),
    startupLockFile: path.join(root, "startup.lock"),
  };
}

export function assertWindows() {
  if (process.platform !== "win32" && process.env.WINDOWS_BROWSER_BRIDGE_ALLOW_NON_WINDOWS !== "1") {
    throw new Error("Windows Browser Draft Runtime requires Windows (set WINDOWS_BROWSER_BRIDGE_ALLOW_NON_WINDOWS=1 only for contract tests)");
  }
}

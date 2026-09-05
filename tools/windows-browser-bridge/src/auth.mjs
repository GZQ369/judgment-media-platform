import crypto from "node:crypto";
import fs from "node:fs/promises";
import { ensureDir } from "./state.mjs";

export async function getToken(config) {
  if (process.env.WINDOWS_BROWSER_BRIDGE_TOKEN) return process.env.WINDOWS_BROWSER_BRIDGE_TOKEN;
  try { return (await fs.readFile(config.tokenFile, "utf8")).trim(); } catch (error) { if (error.code !== "ENOENT") throw error; }
  await ensureDir(config.root);
  const token = crypto.randomBytes(32).toString("hex");
  await fs.writeFile(config.tokenFile, `${token}\n`, { encoding: "utf8", mode: 0o600, flag: "wx" }).catch(async error => {
    if (error.code !== "EEXIST") throw error;
  });
  return (await fs.readFile(config.tokenFile, "utf8")).trim();
}

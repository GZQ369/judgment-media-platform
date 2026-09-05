import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const source = await new Promise((resolve, reject) => {
  let value = "";
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", chunk => { value += chunk; });
  process.stdin.on("end", () => resolve(value));
  process.stdin.on("error", reject);
});
const task = path.join(os.tmpdir(), `ego-launcher-fixture-${process.pid}.mjs`);
await fs.writeFile(task, source, "utf8");
globalThis.cliLog = value => process.stdout.write(`${String(value)}\n`);
try { await import(pathToFileURL(task).href); } finally { await fs.rm(task, { force: true }); }

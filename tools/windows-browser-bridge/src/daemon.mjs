import { getConfig, assertWindows } from "./config.mjs";
import { getToken } from "./auth.mjs";
import { BrowserBridge } from "./bridge.mjs";
import { RPCServer } from "./rpc-server.mjs";
import { PlaywrightBrowserDriver } from "./browser.mjs";
import { fileURLToPath, pathToFileURL } from "node:url";

export async function runDaemon({ config = getConfig(), driver = null, playwright = null } = {}) {
  assertWindows();
  const token = await getToken(config);
  let resolvedDriver = driver;
  if (!resolvedDriver) {
    const module = playwright || await import("playwright-core");
    resolvedDriver = new PlaywrightBrowserDriver(config, module);
  }
  const bridge = new BrowserBridge({ config, driver: resolvedDriver });
  await bridge.start();
  let server;
  let stopping = false;
  server = new RPCServer({ pipeName: config.pipeName, token, bridge, onShutdown: () => bridge.stop() });
  await server.start();
  resolvedDriver.onUnhealthy = () => {
    if (stopping) return;
    stopping = true;
    server.stop().finally(() => process.exit(1));
  };
  const stop = async () => {
    if (stopping) return;
    stopping = true;
    await server.stop();
  };
  process.once("SIGINT", stop); process.once("SIGTERM", stop);
  return { server, bridge, token };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  runDaemon().catch(error => { process.stderr.write(`${error.stack || error}\n`); process.exitCode = 1; });
}

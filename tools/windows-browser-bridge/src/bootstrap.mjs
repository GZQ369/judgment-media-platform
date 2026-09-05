import { RPCClient } from "./client.mjs";
import { getConfig } from "./config.mjs";
import { BridgeError } from "./errors.mjs";

const config = getConfig();
const sessionId = process.env.WINDOWS_BROWSER_BRIDGE_SESSION_ID;
if (!sessionId) throw new Error("WINDOWS_BROWSER_BRIDGE_SESSION_ID is required");
const client = new RPCClient({ ...config, token: process.env.WINDOWS_BROWSER_BRIDGE_TOKEN, sessionId });

const call = (method, params = {}) => client.request(method, params);
globalThis.useOrCreateTaskSpace = ref => call("taskSpace.use", { ref });
globalThis.claimTaskSpace = ref => call("taskSpace.claim", { ref });
globalThis.listTabs = () => call("tabs.list");
globalThis.switchTab = targetId => call("tab.switch", { targetId });
globalThis.openOrReuseTab = (url, options) => call("tab.open", { url, options });
globalThis.pageInfo = () => call("page.info");
globalThis.js = source => call("page.js", { source });
globalThis.cdp = (method, params) => call("page.cdp", { method, params });
globalThis.click = (target, options) => call("page.click", { target, options });
globalThis.hover = (target, options) => call("page.hover", { target, options });
globalThis.pressKey = key => call("page.press", { key });
globalThis.uploadFile = (selector, filePath) => call("page.upload", { selector, path: filePath });
globalThis.gotoAndWait = (url, options) => call("page.goto", { url, options });
globalThis.wait = seconds => call("wait", { seconds });
globalThis.cliLog = value => process.stdout.write(`${String(value)}\n`);
globalThis.BridgeError = BridgeError;

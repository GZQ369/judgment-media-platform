import path from "node:path";
import { BridgeError } from "./errors.mjs";
import { assertAllowedCDP, guardSource, isFinalPublishLabel, normalizeLabel, validateUploadPath } from "./security.mjs";

const executableCandidates = env => [
  env.LOCALAPPDATA && path.join(env.LOCALAPPDATA, "Google", "Chrome", "Application", "chrome.exe"),
  env.PROGRAMFILES && path.join(env.PROGRAMFILES, "Google", "Chrome", "Application", "chrome.exe"),
  env["PROGRAMFILES(X86)"] && path.join(env["PROGRAMFILES(X86)"], "Google", "Chrome", "Application", "chrome.exe"),
  env.LOCALAPPDATA && path.join(env.LOCALAPPDATA, "Microsoft", "Edge", "Application", "msedge.exe"),
  env.PROGRAMFILES && path.join(env.PROGRAMFILES, "Microsoft", "Edge", "Application", "msedge.exe"),
].filter(Boolean);

export async function findBrowserExecutable(config, fsImpl = null) {
  if (config.executablePath) return config.executablePath;
  const fs = fsImpl || await import("node:fs/promises");
  for (const candidate of executableCandidates(process.env)) {
    try { await fs.access(candidate); return candidate; } catch {}
  }
  throw new BridgeError("BROWSER_NOT_FOUND", "Chrome or Edge executable was not found");
}

export class PlaywrightBrowserDriver {
  constructor(config, playwright) {
    this.config = config;
    this.playwright = playwright;
    this.context = null;
    this.pages = new Map();
    this.spaces = new Map();
    this.nextPageId = 1;
    this.queues = new Map();
    this.inputQueue = Promise.resolve();
    this.cdpSessions = new Map();
    this.healthy = false;
    this.unhealthyReason = null;
    this.onUnhealthy = null;
    this.stopping = false;
  }

  async start() {
    const executablePath = await findBrowserExecutable(this.config);
    await (await import("node:fs/promises")).mkdir(this.config.profileDir, { recursive: true });
    this.context = await this.playwright.chromium.launchPersistentContext(this.config.profileDir, {
      executablePath,
      headless: false,
      args: ["--no-first-run", "--no-default-browser-check"],
    });
    this.context.on("close", () => {
      if (this.stopping) {
        this.healthy = false;
        return;
      }
      this.markUnhealthy("browser context closed");
    });
    await this.context.addInitScript({ content: guardSource() });
    for (const page of this.context.pages()) this.registerPage(page, null);
    this.context.on("page", page => this.registerPage(page, null));
    this.healthy = true;
    return { executablePath, profileDir: this.config.profileDir };
  }

  isHealthy() { return this.healthy === true && Boolean(this.context); }

  markUnhealthy(reason) {
    this.healthy = false;
    this.unhealthyReason = reason;
    this.onUnhealthy?.(reason);
  }

  spaceState(spaceId) {
    if (!this.spaces.has(spaceId)) this.spaces.set(spaceId, { activePageId: null, pages: new Map() });
    return this.spaces.get(spaceId);
  }

  registerPage(page, spaceId, logicalId = null) {
    const existing = this.pages.get(page);
    if (existing) {
      if (spaceId !== null && existing.spaceId === null) {
        existing.spaceId = spaceId;
        existing.logicalId = logicalId || `page-${spaceId}-${this.nextPageId++}`;
        this.spaceState(spaceId).pages.set(existing.logicalId, page);
      } else if (spaceId !== null && existing.spaceId !== spaceId) throw new BridgeError("PAGE_OWNERSHIP_CONFLICT", "page already belongs to another task space");
      return existing.logicalId;
    }
    const id = logicalId || `page-${spaceId ?? "unassigned"}-${this.nextPageId++}`;
    this.pages.set(page, { logicalId: id, spaceId });
    if (spaceId !== null) this.spaceState(spaceId).pages.set(id, page);
    page.on("close", () => {
      const owner = this.pages.get(page);
      this.pages.delete(page);
      if (owner?.spaceId !== null) {
        const state = this.spaceState(owner.spaceId);
        state.pages.delete(owner.logicalId);
        if (state.activePageId === owner.logicalId) state.activePageId = null;
      }
      for (const key of this.queues.keys()) if (key.endsWith(`:${id}`)) this.queues.delete(key);
      const session = this.cdpSessions.get(page);
      this.cdpSessions.delete(page);
      session?.detach().catch(() => {});
    });
    page.addInitScript({ content: guardSource() }).catch(() => {});
    page.evaluate(guardSource()).catch(() => {});
    return id;
  }

  pageById(spaceId, targetId) {
    const page = this.spaceState(spaceId).pages.get(targetId);
    if (page) return page;
    throw new BridgeError("PAGE_NOT_FOUND", `page not found: ${targetId}`);
  }

  currentPage(spaceId) {
    const state = this.spaceState(spaceId);
    if (!state.activePageId) throw new BridgeError("PAGE_NOT_SELECTED", "no active page in task space");
    return this.pageById(spaceId, state.activePageId);
  }

  serial(spaceId, targetId, task) {
    const key = `${spaceId}:${targetId}`;
    const previous = this.queues.get(key) || Promise.resolve();
    const next = previous.catch(() => {}).then(task);
    let observed;
    const tracked = next.finally(() => { if (this.queues.get(key) === observed) this.queues.delete(key); });
    observed = tracked.catch(() => {});
    this.queues.set(key, observed);
    return next;
  }

  input(task) { const next = this.inputQueue.catch(() => {}).then(task); this.inputQueue = next.catch(() => {}); return next; }

  async listTabs(spaceId) {
    return [...this.spaceState(spaceId).pages].map(([targetId, page]) => ({ targetId, url: page.url(), title: "" }));
  }

  async restorePages(spaceId, records = []) {
    const state = this.spaceState(spaceId);
    for (const record of records) {
      if (!record?.lastURL || record.lastURL === "about:blank") continue;
      const existing = [...state.pages.values()].find(page => page.url() === record.lastURL);
      const page = existing || await this.context.newPage();
      const targetId = this.registerPage(page, spaceId, record.logicalId);
      if (page.url() !== record.lastURL) await page.goto(record.lastURL, { waitUntil: "domcontentloaded", timeout: 45000 });
      state.activePageId = targetId;
    }
  }

  async switchTab(spaceId, targetId) { this.pageById(spaceId, targetId); this.spaceState(spaceId).activePageId = targetId; return { targetId }; }

  activePageId(spaceId) { return this.spaceState(spaceId).activePageId; }

  async openOrReuseTab(spaceId, url, options = {}) {
    const state = this.spaceState(spaceId);
    let page = [...state.pages.values()].find(candidate => candidate.url() === url);
    if (!page) page = await this.context.newPage();
    const targetId = this.registerPage(page, spaceId);
    state.activePageId = targetId;
    if (page.url() !== url) await page.goto(url, { waitUntil: "domcontentloaded", timeout: Number(options.timeout || 45) * 1000 });
    if (options.wait) await page.waitForTimeout(1000);
    await page.evaluate(guardSource()).catch(() => {});
    return { targetId, url: page.url(), title: await page.title().catch(() => "") };
  }

  async pageInfo(spaceId) {
    const page = this.currentPage(spaceId);
    const viewport = page.viewportSize() || { width: 1280, height: 720 };
    return { w: viewport.width, h: viewport.height, dialog: null };
  }

  async js(spaceId, source) {
    if (typeof source !== "string") throw new BridgeError("INVALID_JS", "js source must be a string");
    const state = this.spaceState(spaceId);
    return this.serial(spaceId, state.activePageId, () => this.currentPage(spaceId).evaluate(source));
  }

  async cdp(spaceId, method, params = {}) {
    assertAllowedCDP(method);
    let safeParams = params;
    if (method === "DOM.setFileInputFiles") {
      if (!Array.isArray(params.files) || params.files.length === 0) throw new BridgeError("UPLOAD_PATH_INVALID", "DOM.setFileInputFiles requires media files");
      const files = [];
      for (const file of params.files) files.push(await validateUploadPath(file, this.config));
      safeParams = { ...params, files };
    }
    const page = this.currentPage(spaceId);
    const targetId = this.spaceState(spaceId).activePageId;
    const run = () => this.getCDPSession(page).then(session => session.send(method, safeParams));
    const focusMethod = method === "Page.bringToFront" || method === "Emulation.setFocusEmulationEnabled" || method === "Input.insertText";
    return focusMethod ? this.input(() => this.serial(spaceId, targetId, run)) : this.serial(spaceId, targetId, run);
  }

  async getCDPSession(page) {
    const existing = this.cdpSessions.get(page);
    if (existing) return existing;
    const session = await this.context.newCDPSession(page);
    this.cdpSessions.set(page, session);
    return session;
  }

  async targetLabels(page, selectorOrPoint) {
    return page.evaluate(target => {
      const compact = value => String(value || "").replace(/\s+/g, " ").trim();
      const element = Array.isArray(target) ? document.elementFromPoint(target[0], target[1]) : document.querySelector(target);
      const candidates = [];
      let current = element;
      while (current && candidates.length < 5) { candidates.push(current); current = current.parentElement; }
      return candidates.map(item => compact(item.value || item.getAttribute?.("aria-label") || item.innerText || item.textContent)).filter(Boolean);
    }, Array.isArray(selectorOrPoint) ? selectorOrPoint : selectorOrPoint);
  }

  async assertNotFinal(page, selectorOrPoint) {
    const labels = (await this.targetLabels(page, selectorOrPoint)).map(normalizeLabel);
    const finalLabel = labels.find(isFinalPublishLabel);
    if (finalLabel) throw new BridgeError("FINAL_PUBLISH_BLOCKED", `final publish control is blocked: ${finalLabel}`);
    return labels[0] || "";
  }

  async click(spaceId, selectorOrPoint, options = {}) {
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    const target = Array.isArray(selectorOrPoint) ? selectorOrPoint : String(selectorOrPoint);
    return this.input(() => this.serial(spaceId, state.activePageId, async () => {
      await this.assertNotFinal(page, target);
      if (Array.isArray(target)) await page.mouse.click(target[0], target[1]);
      else await page.locator(target).click({ timeout: Number(options.timeout || 10000) });
      return { ok: true, label: options.label || null };
    }));
  }

  async hover(spaceId, selectorOrPoint, options = {}) {
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    const target = Array.isArray(selectorOrPoint) ? selectorOrPoint : String(selectorOrPoint);
    return this.input(() => this.serial(spaceId, state.activePageId, async () => {
      await this.assertNotFinal(page, target);
      if (Array.isArray(target)) await page.mouse.move(target[0], target[1]);
      else await page.locator(target).hover({ timeout: Number(options.timeout || 10000) });
      return { ok: true };
    }));
  }

  async pressKey(spaceId, key) {
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    return this.input(() => this.serial(spaceId, state.activePageId, async () => {
      if (String(key).toLowerCase() === "enter") {
        const labels = await page.evaluate(() => {
          let element = document.activeElement; const values = [];
          while (element && values.length < 5) { values.push(element.value || element.getAttribute?.("aria-label") || element.innerText || element.textContent); element = element.parentElement; }
          return values.filter(Boolean);
        });
        const finalLabel = labels.map(normalizeLabel).find(isFinalPublishLabel);
        if (finalLabel) throw new BridgeError("FINAL_PUBLISH_BLOCKED", `final publish control is blocked: ${finalLabel}`);
      }
      await page.keyboard.press(key); return { ok: true };
    }));
  }

  async uploadFile(spaceId, selector, inputPath) {
    const resolved = await validateUploadPath(inputPath, this.config);
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    return this.serial(spaceId, state.activePageId, async () => { await page.locator(selector).setInputFiles(resolved); return { ok: true, path: resolved.replaceAll("\\", "/").split("/").pop() }; });
  }

  async gotoAndWait(spaceId, url, options = {}) { return this.openOrReuseTab(spaceId, url, { ...options, wait: true }); }
  async wait(seconds) { await new Promise(resolve => setTimeout(resolve, Math.max(0, Number(seconds) * 1000))); return { ok: true }; }
  async close() {
    this.stopping = true;
    this.healthy = false;
    for (const session of this.cdpSessions.values()) await session.detach().catch(() => {});
    this.cdpSessions.clear();
    await this.context?.close();
    this.context = null;
  }
}

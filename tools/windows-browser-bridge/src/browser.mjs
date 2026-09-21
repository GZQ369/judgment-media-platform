import path from "node:path";
import crypto from "node:crypto";
import { BridgeError } from "./errors.mjs";
import { assertAllowedCDP, guardSource, isFinalPublishLabel, normalizeLabel, validateUploadPath } from "./security.mjs";

const PUBLICATION_POLL_ATTEMPTS = 8;
const PUBLICATION_POLL_DELAY_MS = 500;

const PLATFORM_RULES = Object.freeze({
  douyin: Object.freeze({
    hosts: new Set(["creator.douyin.com", "www.douyin.com", "m.douyin.com"]),
    detail: /^\/(?:video|note)\/([^/?#]+)/i,
    statuses: new Map([
      ["审核中", "reviewing"], ["作品审核中", "reviewing"], ["发布审核中", "reviewing"],
      ["发布成功", "submitted"], ["作品已发布", "published"], ["已发布", "published"],
    ]),
  }),
  xiaohongshu: Object.freeze({
    hosts: new Set(["creator.xiaohongshu.com", "www.xiaohongshu.com", "xhslink.com"]),
    detail: /^\/(?:explore|discovery\/item)\/([^/?#]+)/i,
    statuses: new Map([
      ["审核中", "reviewing"], ["笔记审核中", "reviewing"], ["发布审核中", "reviewing"],
      ["发布成功", "submitted"], ["笔记已发布", "published"], ["已发布", "published"],
    ]),
  }),
  bilibili: Object.freeze({
    hosts: new Set(["member.bilibili.com", "www.bilibili.com"]),
    detail: /^\/video\/([^/?#]+)/i,
    statuses: new Map([
      ["审核中", "reviewing"], ["稿件审核中", "reviewing"], ["投稿审核中", "reviewing"],
      ["投稿成功", "submitted"], ["稿件已发布", "published"], ["已发布", "published"],
    ]),
  }),
});

function compact(value) { return String(value || "").replace(/\s+/g, " ").trim(); }

function boundedString(value, max = 512) {
  const result = compact(value);
  return result.length > max ? result.slice(0, max) : result;
}

function trustedURL(platform, value) {
  const rules = PLATFORM_RULES[platform];
  if (!rules || typeof value !== "string") return null;
  let parsed;
  try { parsed = new URL(value); } catch { return null; }
  if ((parsed.protocol !== "https:" && parsed.protocol !== "http:") || parsed.username || parsed.password) return null;
  if (!rules.hosts.has(parsed.hostname.toLowerCase())) return null;
  return parsed;
}

function detailEvidence(platform, value) {
  const parsed = trustedURL(platform, value);
  const rules = PLATFORM_RULES[platform];
  if (!parsed || !rules) return null;
  const match = parsed.pathname.match(rules.detail);
  if (!match || !match[1]) return null;
  let externalID;
  try { externalID = boundedString(decodeURIComponent(match[1])); } catch { return null; }
  return externalID ? { externalID, externalURL: parsed.href } : null;
}

function normalizePublicationStatus(platform, value) {
  const rules = PLATFORM_RULES[platform];
  if (!rules) return "";
  return rules.statuses.get(compact(value)) || "";
}

function unknownPublicationEvidence() {
  return { status: "unknown", externalID: "", externalURL: "", evidenceSource: "unknown" };
}

function publicationIdentity(platform, raw = {}) {
  const detail = detailEvidence(platform, boundedString(raw.url));
  const linked = detailEvidence(platform, boundedString(raw.externalURL));
  const ids = [boundedString(raw.externalID), detail?.externalID, linked?.externalID].filter(Boolean);
  if (new Set(ids).size > 1) return null;
  return { externalID: ids[0] || "", externalURL: linked?.externalURL || detail?.externalURL || "" };
}

function samePublicationIdentity(left = {}, right = {}) {
  return Boolean(
    (left.externalID && right.externalID && left.externalID === right.externalID)
    || (left.externalURL && right.externalURL && left.externalURL === right.externalURL),
  );
}

function trustedPublicationEvidence(platform, raw = {}, { mode = "reconcile", correlation = null } = {}) {
  const currentURL = boundedString(raw.url);
  const pageURL = trustedURL(platform, currentURL);
  if (!pageURL || !correlation) return unknownPublicationEvidence();

  const detail = detailEvidence(platform, currentURL);
  const identity = publicationIdentity(platform, raw);
  if (!identity) return unknownPublicationEvidence();
  const externalURL = identity.externalURL;
  const externalID = identity.externalID;
  const status = normalizePublicationStatus(platform, raw.status) || normalizePublicationStatus(platform, raw.statusText);
  if (!externalID && !externalURL) return unknownPublicationEvidence();

  if (mode === "publish") {
    const baseline = correlation.baseline || {};
    const baselineIdentity = publicationIdentity(platform, baseline);
    if (!trustedURL(platform, baseline.url) || !baselineIdentity || samePublicationIdentity(identity, baselineIdentity)) {
      return unknownPublicationEvidence();
    }
  } else if (mode === "reconcile" && !samePublicationIdentity(identity, correlation)) {
    return unknownPublicationEvidence();
  }

  if (detail) return { status: "published", ...detail, evidenceSource: "platform-detail-url" };
  if (!status) return unknownPublicationEvidence();
  return { status, externalID, externalURL, evidenceSource: "platform-dom" };
}

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
    this.restoredSpaces = new Set();
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
    if (!this.spaces.has(spaceId)) this.spaces.set(spaceId, { activePageId: null, pages: new Map(), submissionCorrelation: null });
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
    page.on("close", () => this.unregisterPage(page));
    page.addInitScript({ content: guardSource() }).catch(() => {});
    page.evaluate(guardSource()).catch(() => {});
    return id;
  }

  unregisterPage(page) {
    const owner = this.pages.get(page);
    if (!owner) return;
    this.pages.delete(page);
    if (owner.spaceId !== null) {
      const state = this.spaceState(owner.spaceId);
      state.pages.delete(owner.logicalId);
      if (state.activePageId === owner.logicalId) state.activePageId = null;
    }
    for (const key of this.queues.keys()) if (key.endsWith(`:${owner.logicalId}`)) this.queues.delete(key);
    const session = this.cdpSessions.get(page);
    this.cdpSessions.delete(page);
    session?.detach().catch(() => {});
  }

  async discardPage(page) {
    try { await page.close?.({ runBeforeUnload: false }); } catch {}
    this.unregisterPage(page);
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
    const state = this.spaceState(spaceId);
    const entries = [...state.pages];
    const activeIndex = entries.findIndex(([targetId]) => targetId === state.activePageId);
    if (activeIndex > 0) {
      const [active] = entries.splice(activeIndex, 1);
      entries.unshift(active);
    }
    return entries.map(([targetId, page]) => ({ targetId, url: page.url(), title: "" }));
  }

  async restorePages(spaceId, records = [], { activePageId = null, activeURL = "" } = {}) {
    const state = this.spaceState(spaceId);

    if (state.pages.size > 0) {
      if (!state.activePageId || !state.pages.has(state.activePageId)) {
        const activeByURL = activeURL
          ? [...state.pages].find(([, page]) => page.url() === activeURL)?.[0]
          : null;
        state.activePageId = activePageId && state.pages.has(activePageId)
          ? activePageId
          : activeByURL || state.pages.keys().next().value || null;
      }
      this.restoredSpaces.add(spaceId);
      return;
    }
    if (this.restoredSpaces.has(spaceId)) return;

    const created = [];
    try {
      const restored = [];
      for (const record of records) {
        if (!record?.lastURL || record.lastURL === "about:blank") continue;
        const page = await this.context.newPage();
        created.push(page);
        const targetId = this.registerPage(page, spaceId, record.logicalId);
        if (page.url() !== record.lastURL) await page.goto(record.lastURL, { waitUntil: "domcontentloaded", timeout: 45000 });
        restored.push({ targetId, persistedURL: record.lastURL });
      }
      const activeByURL = activeURL ? restored.find(item => item.persistedURL === activeURL)?.targetId : null;
      state.activePageId = activePageId && state.pages.has(activePageId)
        ? activePageId
        : activeByURL || restored[0]?.targetId || null;
      this.restoredSpaces.add(spaceId);
    } catch (error) {
      await Promise.allSettled(created.map(page => this.discardPage(page)));
      state.activePageId = null;
      throw error;
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

  async publicationSnapshot(page) {
    return page.evaluate(() => {
      const firstAttribute = (selectors, attributes) => {
        for (const selector of selectors) {
          const element = document.querySelector(selector);
          if (!element) continue;
          for (const attribute of attributes) {
            const value = element.getAttribute?.(attribute);
            if (value) return value;
          }
        }
        return "";
      };
      const statusValues = [...document.querySelectorAll(
        "[data-ego-publication-status], [data-publication-status], [data-submission-status]",
      )].flatMap(element => [
        element.getAttribute("data-ego-publication-status"),
        element.getAttribute("data-publication-status"),
        element.getAttribute("data-submission-status"),
        element.textContent,
      ]).filter(Boolean);
      return {
        url: location.href,
        statusText: statusValues[0] || "",
        status: statusValues[0] || "",
        externalID: firstAttribute(
          ["[data-work-id]", "[data-publication-id]", "[data-item-id]", "[data-video-id]", "[data-post-id]"],
          ["data-work-id", "data-publication-id", "data-item-id", "data-video-id", "data-post-id", "content"],
        ),
        externalURL: firstAttribute(["[data-publication-url]", "[data-item-url]"], ["data-publication-url", "data-item-url", "href"]),
      };
    }).catch(() => ({}));
  }

  publicCorrelation(correlation) {
    if (!correlation) return null;
    return {
      version: 1,
      id: correlation.id,
      taskSpaceId: correlation.taskSpaceId,
      pageId: correlation.pageId,
    };
  }

  async publicationEvidence(page, platform, taskSpaceId, targetId, { mode = "reconcile", correlation = null } = {}) {
    const raw = await this.publicationSnapshot(page);
    const scopedCorrelation = correlation
      && String(correlation.taskSpaceId) === String(taskSpaceId)
      && String(correlation.pageId) === String(targetId)
      ? correlation
      : null;
    const evidence = trustedPublicationEvidence(platform, { ...raw, url: raw.url || page.url() }, { mode, correlation: scopedCorrelation });
    return {
      platform: String(platform || ""),
      taskSpaceId: String(taskSpaceId),
      status: evidence.status,
      externalID: evidence.externalID,
      externalURL: evidence.externalURL,
      evidenceSource: evidence.evidenceSource,
      evidenceState: evidence.status === "unknown" ? "unknown" : "identified",
      submissionCorrelation: this.publicCorrelation(correlation),
      dom: {
        url: boundedString(raw.url || page.url()),
        status: evidence.status,
        externalID: evidence.externalID,
        externalURL: evidence.externalURL,
        evidenceSource: evidence.evidenceSource,
        submissionCorrelation: this.publicCorrelation(correlation),
      },
    };
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

  async publishOnce(spaceId, expectedLabel, authorized = false, platform = "") {
    const label = normalizeLabel(expectedLabel);
    if (authorized !== true) throw new BridgeError("PUBLISH_AUTH_REQUIRED", "current-run final publish authorization is required");
    if (!isFinalPublishLabel(label)) throw new BridgeError("INVALID_PUBLISH_LABEL", `unsupported final publish label: ${label}`);
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    return this.input(() => this.serial(spaceId, state.activePageId, async () => {
      const targetId = state.activePageId;
      const target = page.getByText(label, { exact: true }).last();
      const count = await target.count();
      if (count !== 1) throw new BridgeError("PUBLISH_CONTROL_NOT_FOUND", `expected one visible final publish control: ${label}`);
      if (!await target.isVisible() || !await target.isEnabled()) throw new BridgeError("PUBLISH_CONTROL_NOT_READY", `final publish control is not ready: ${label}`);
      const correlation = {
        id: crypto.randomUUID(),
        taskSpaceId: String(spaceId),
        pageId: String(targetId),
        baseline: await this.publicationSnapshot(page),
        externalID: "",
        externalURL: "",
      };
      state.submissionCorrelation = correlation;
      await page.evaluate(value => {
        const guard = window.__JUDGMENT_MEDIA_FINAL_PUBLISH_GUARD__;
        if (!guard?.armed) throw new Error("final publish guard is not armed");
        guard.authorization = { label: value, expiresAt: Date.now() + 5000 };
      }, label);
      try {
        await target.click({ timeout: 10000 });
        // A click is not evidence of submission. Poll only read-only,
        // platform-bound evidence; an unknown result is never retried by
        // clicking again.
        let evidence = await this.publicationEvidence(page, platform, spaceId, targetId, { mode: "publish", correlation });
        for (let attempt = 0; attempt < PUBLICATION_POLL_ATTEMPTS && evidence.status === "unknown"; attempt += 1) {
          await page.waitForTimeout(PUBLICATION_POLL_DELAY_MS);
          evidence = await this.publicationEvidence(page, platform, spaceId, targetId, { mode: "publish", correlation });
        }
        if (evidence.status !== "unknown") {
          correlation.externalID = evidence.externalID;
          correlation.externalURL = evidence.externalURL;
        }
        return {
          ok: true,
          finalPublishClicked: true,
          label,
          taskSpaceId: String(spaceId),
          platform: String(platform || ""),
          status: evidence.status,
          externalID: evidence.externalID,
          externalURL: evidence.externalURL,
          evidenceSource: evidence.evidenceSource,
          evidenceState: evidence.evidenceState,
          submissionCorrelation: evidence.submissionCorrelation,
          page: { url: page.url() },
          dom: evidence.dom,
        };
      } finally {
        await page.evaluate(() => {
          const guard = window.__JUDGMENT_MEDIA_FINAL_PUBLISH_GUARD__;
          if (guard) guard.authorization = null;
        }).catch(() => {});
      }
    }));
  }

  async inspectPublication(spaceId, platform = "") {
    const page = this.currentPage(spaceId); const state = this.spaceState(spaceId);
    const correlation = state.submissionCorrelation;
    return this.serial(spaceId, state.activePageId, () => this.publicationEvidence(page, platform, spaceId, state.activePageId, { mode: "reconcile", correlation }));
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
    this.restoredSpaces.clear();
    await this.context?.close();
    this.context = null;
  }
}

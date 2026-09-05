import { TaskSpaceStore, LeaseManager } from "./state.mjs";
import { BridgeError } from "./errors.mjs";
import crypto from "node:crypto";

export class BrowserBridge {
  constructor({ config, driver, store = new TaskSpaceStore(config), leases = null, now = () => Date.now() }) {
    this.config = config;
    this.driver = driver;
    this.store = store;
    this.leaseTTLms = Number(config.leaseTTLms || 600000);
    this.now = now;
    this.leases = leases || new LeaseManager({ ttlMs: this.leaseTTLms, now });
    this.sessions = new Map();
    this.active = new Map();
    this.switchQueues = new Map();
    this.reaper = null;
  }

  async start() {
    await this.store.load();
    if (this.driver.start) await this.driver.start();
    this.reaper = setInterval(() => this.reapExpired(), Math.max(1000, Math.floor(this.leaseTTLms / 2)));
    this.reaper.unref?.();
  }

  isHealthy() { return typeof this.driver.isHealthy === "function" ? this.driver.isHealthy() : false; }

  reapExpired(now = this.now()) {
    for (const [sessionId, session] of this.sessions) {
      if (now - session.lastSeen > this.leaseTTLms) {
        this.leases.releaseSession(sessionId);
        this.active.delete(sessionId);
        this.sessions.delete(sessionId);
      }
    }
    for (const expired of this.leases.reap(now)) {
      this.active.delete(expired.sessionId);
    }
  }

  requireSession(sessionId) {
    if (!sessionId || !this.sessions.has(sessionId)) throw new BridgeError("SESSION_NOT_FOUND", "bridge session is not open");
  }

  touchSession(sessionId) {
    this.requireSession(sessionId);
    const session = this.sessions.get(sessionId);
    session.lastSeen = this.now();
    this.leases.touch(sessionId);
  }

  serializeSwitch(sessionId, task) {
    const previous = this.switchQueues.get(sessionId) || Promise.resolve();
    const next = previous.catch(() => {}).then(task);
    let observed;
    const tracked = next.finally(() => {
      if (this.switchQueues.get(sessionId) === observed) this.switchQueues.delete(sessionId);
    });
    observed = tracked.catch(() => {});
    this.switchQueues.set(sessionId, observed);
    return next;
  }

  async switchTaskSpace(sessionId, params = {}) {
    return this.serializeSwitch(sessionId, async () => {
      this.requireSession(sessionId);
      const previous = this.active.get(sessionId) || null;
      const space = await this.store.useOrCreate(params.ref, params.platform);
      this.requireSession(sessionId);

      const sameSpace = previous?.id === space.id;
      if (!sameSpace) this.leases.acquire(space.id, sessionId);
      try {
        await this.driver.restorePages?.(space.id, space.pages || []);
        this.requireSession(sessionId);
        const current = this.active.get(sessionId) || null;
        if (current !== previous) {
          throw new BridgeError("TASK_SPACE_SWITCH_CONFLICT", "task space selection changed while restoring pages");
        }
        if (!this.leases.has(space.id, sessionId)) {
          throw new BridgeError("TASK_SPACE_LEASE_LOST", "task space lease was lost while restoring pages");
        }
        this.active.set(sessionId, space);
        if (previous && !sameSpace) this.leases.release(previous.id, sessionId);
        return { id: space.id, name: space.name };
      } catch (error) {
        if (!sameSpace) this.leases.release(space.id, sessionId);
        throw error;
      }
    });
  }

  requireSpace(sessionId) {
    this.requireSession(sessionId);
    const taskSpace = this.active.get(sessionId);
    if (!taskSpace) throw new BridgeError("TASK_SPACE_NOT_SELECTED", "task space is not selected");
    if (!this.leases.has(taskSpace.id, sessionId)) throw new BridgeError("TASK_SPACE_LEASE_REQUIRED", "task space lease is required");
    return taskSpace;
  }

  async dispatch(method, params, sessionId) {
    this.reapExpired();
    if (method === "health") return { ok: this.isHealthy(), service: "windows-browser-bridge", version: 1, reason: this.driver.unhealthyReason || null };
    if (method === "session.open") {
      if (sessionId) throw new BridgeError("SESSION_ID_FORBIDDEN", "session.open must not accept a client-supplied session id");
      const issuedSessionId = crypto.randomUUID();
      const timestamp = this.now();
      this.sessions.set(issuedSessionId, { createdAt: timestamp, lastSeen: timestamp });
      return { ok: true, sessionId: issuedSessionId };
    }
    if (method === "session.close") {
      this.requireSession(sessionId);
      this.leases.releaseSession(sessionId); this.active.delete(sessionId); this.sessions.delete(sessionId); return { ok: true };
    }
    if (method === "status") return { ok: this.isHealthy(), sessions: this.sessions.size, taskSpaces: (await this.store.load()).taskSpaces.length, browser: this.isHealthy(), reason: this.driver.unhealthyReason || null };
    if (method === "shutdown") return { ok: true, shutdown: true, requestShutdown: true };
    this.touchSession(sessionId);

    if (method === "taskSpace.use" || method === "taskSpace.claim") {
      return this.switchTaskSpace(sessionId, params);
    }
    const taskSpace = this.requireSpace(sessionId);
    if (method === "tabs.list") return this.driver.listTabs(taskSpace.id);
    if (method === "tab.switch") {
      const result = await this.driver.switchTab(taskSpace.id, params.targetId);
      await this.persistPage(taskSpace);
      return result;
    }
    if (method === "tab.open") {
      const result = await this.driver.openOrReuseTab(taskSpace.id, params.url, params.options || {});
      await this.persistPage(taskSpace);
      return result;
    }
    if (method === "page.info") return this.driver.pageInfo(taskSpace.id);
    if (method === "page.js") return this.driver.js(taskSpace.id, params.source);
    if (method === "page.cdp") return this.driver.cdp(taskSpace.id, params.method, params.params || {});
    if (method === "page.click") return this.driver.click(taskSpace.id, params.target, params.options || {});
    if (method === "page.hover") return this.driver.hover(taskSpace.id, params.target, params.options || {});
    if (method === "page.press") return this.driver.pressKey(taskSpace.id, params.key);
    if (method === "page.upload") return this.driver.uploadFile(taskSpace.id, params.selector, params.path);
    if (method === "page.goto") { const result = await this.driver.gotoAndWait(taskSpace.id, params.url, params.options || {}); await this.persistPage(taskSpace); return result; }
    if (method === "wait") return this.driver.wait(params.seconds);
    throw new BridgeError("METHOD_NOT_FOUND", `unknown RPC method: ${method}`);
  }

  async persistPage(taskSpace) {
    const tabs = await this.driver.listTabs(taskSpace.id);
    const activePageId = typeof this.driver.activePageId === "function" ? this.driver.activePageId(taskSpace.id) : null;
    taskSpace.lastURL = tabs.find(tab => tab.targetId === activePageId)?.url || taskSpace.lastURL;
    taskSpace.pages = tabs.map(tab => ({ logicalId: tab.targetId, lastURL: tab.url }));
    await this.store.update(taskSpace);
  }

  async stop() {
    if (this.reaper) clearInterval(this.reaper);
    for (const sessionId of this.sessions.keys()) this.leases.releaseSession(sessionId);
    this.sessions.clear(); this.active.clear(); this.switchQueues.clear(); await this.driver.close?.();
  }
}

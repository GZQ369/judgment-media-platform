import { TaskSpaceStore, LeaseManager } from "./state.mjs";
import { BridgeError } from "./errors.mjs";
import crypto from "node:crypto";

export class BrowserBridge {
  constructor({ config, driver, store = new TaskSpaceStore(config), leases = null, now = () => Date.now() }) {
    this.config = config;
    this.driver = driver;
    this.store = store;
    this.leaseTTLms = Number(config.leaseTTLms || 600000);
    this.operationDrainTimeoutMs = Math.max(1, Number(config.operationDrainTimeoutMs || 15000));
    this.now = now;
    this.leases = leases || new LeaseManager({ ttlMs: this.leaseTTLms, now });
    this.sessions = new Map();
    this.active = new Map();
    this.switchQueues = new Map();
    this.reapTasks = new Set();
    this.reaper = null;
    this.stopPromise = null;
    this.stopping = false;
    this.lastReaperError = null;
  }

  async start() {
    await this.store.load();
    if (this.driver.start) await this.driver.start();
    this.reaper = setInterval(() => this.reapExpired(), Math.max(1000, Math.floor(this.leaseTTLms / 2)));
    this.reaper.unref?.();
  }

  isHealthy() { return typeof this.driver.isHealthy === "function" ? this.driver.isHealthy() : false; }

  reapExpired(now = this.now()) {
    const closing = [];
    for (const [sessionId, session] of this.sessions) {
      if (!session.closing && now - session.lastSeen > this.leaseTTLms) closing.push(this.closeSession(sessionId, { reason: "ttl", join: true }));
    }
    if (closing.length === 0) return Promise.resolve([]);
    const task = Promise.all(closing).then(results => {
      const failed = results.find(result => !result.ok);
      if (failed) this.lastReaperError = failed.persistenceError || "expired session persistence failed";
      return results;
    });
    this.reapTasks.add(task);
    task.catch(error => { this.lastReaperError = String(error?.message || error); }).finally(() => this.reapTasks.delete(task));
    return task;
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

  beginSessionOperation(sessionId) {
    if (this.stopping) throw new BridgeError("DAEMON_STOPPING", "browser bridge is stopping");
    this.requireSession(sessionId);
    const session = this.sessions.get(sessionId);
    if (session.closing) throw new BridgeError("SESSION_CLOSING", "bridge session is closing");
    let resolve;
    const settled = new Promise(done => { resolve = done; });
    const operation = { settled, resolve };
    session.inFlight.add(operation);
    return () => {
      if (!session.inFlight.delete(operation)) return;
      operation.resolve();
      this.scheduleDeferredSessionCleanup(sessionId, session);
    };
  }

  async runSessionOperation(sessionId, task) {
    const finish = this.beginSessionOperation(sessionId);
    try {
      this.touchSession(sessionId);
      return await task();
    } finally {
      finish();
    }
  }

  closeSession(sessionId, { reason = "client", join = false } = {}) {
    this.requireSession(sessionId);
    const session = this.sessions.get(sessionId);
    if (session.closing) {
      if (join && session.closePromise) return session.closePromise;
      throw new BridgeError("SESSION_CLOSING", "bridge session is closing");
    }
    session.closing = true;
    session.closeReason = reason;
    session.closePromise = Promise.resolve().then(() => this.finishCloseSession(sessionId, session));
    return session.closePromise;
  }

  async finishCloseSession(sessionId, session) {
    let drainTimedOut = false;
    if (session.inFlight.size > 0) {
      try {
        await this.withTimeout(
          Promise.allSettled([...session.inFlight].map(operation => operation.settled)),
          this.operationDrainTimeoutMs,
          "session operation drain timed out",
        );
      } catch (error) {
        if (error?.code !== "OPERATION_DRAIN_TIMEOUT") throw error;
        drainTimedOut = true;
      }
    }

    // A timed-out operation may still be using the shared browser context. Keep
    // the session and every lease it owns quarantined until that operation ends.
    if (drainTimedOut) {
      session.quarantined = true;
      this.scheduleDeferredSessionCleanup(sessionId, session);
      return {
        ok: false,
        persisted: false,
        persistenceError: null,
        drainTimedOut: true,
        quarantined: true,
        reason: session.closeReason,
      };
    }

    return this.persistAndFinalizeSession(sessionId, session, false);
  }

  async persistAndFinalizeSession(sessionId, session, deferred) {
    const taskSpace = this.active.get(sessionId) || null;
    let persistenceError = null;
    try {
      if (taskSpace && this.leases.has(taskSpace.id, sessionId)) {
        await this.withTimeout(this.persistPage(taskSpace), this.operationDrainTimeoutMs, "session state persistence timed out");
      }
    } catch (error) {
      persistenceError = String(error?.message || error);
    } finally {
      this.finalizeSession(sessionId, session);
    }
    return {
      ok: persistenceError === null && !deferred,
      persisted: taskSpace ? persistenceError === null : null,
      persistenceError,
      drainTimedOut: false,
      reason: session.closeReason,
    };
  }

  finalizeSession(sessionId, session) {
    if (session.finalized) return;
    session.finalized = true;
    this.leases.releaseSession(sessionId);
    this.active.delete(sessionId);
    if (this.sessions.get(sessionId) === session) this.sessions.delete(sessionId);
  }

  scheduleDeferredSessionCleanup(sessionId, session) {
    if (!session.quarantined || session.cleanupPromise || session.finalized) return;
    session.cleanupPromise = Promise.resolve().then(async () => {
      await Promise.allSettled([...session.inFlight].map(operation => operation.settled));
      if (session.finalized || this.sessions.get(sessionId) !== session) return;
      await this.persistAndFinalizeSession(sessionId, session, true);
    }).catch(error => {
      session.cleanupError = String(error?.message || error);
      this.finalizeSession(sessionId, session);
    });
  }

  forceFinalizeSession(sessionId, session) {
    if (session.finalized) return;
    session.forceClosed = true;
    for (const operation of session.inFlight) operation.resolve();
    session.inFlight.clear();
    this.finalizeSession(sessionId, session);
  }

  withTimeout(promise, timeoutMs, message) {
    let timer;
    const timeout = new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(new BridgeError("OPERATION_DRAIN_TIMEOUT", message)), timeoutMs);
    });
    return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
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
      const requestedPlatform = String(params.platform || "").trim().toLowerCase();
      if (requestedPlatform && space.platform && space.platform !== requestedPlatform) {
        throw new BridgeError("TASK_SPACE_PLATFORM_MISMATCH", "task space is bound to a different platform");
      }
      if (requestedPlatform && !space.platform) {
        space.platform = requestedPlatform;
        await this.store.update(space);
      }
      this.requireSession(sessionId);

      const sameSpace = previous?.id === space.id;
      if (!sameSpace) this.leases.acquire(space.id, sessionId);
      try {
        await this.driver.restorePages?.(space.id, space.pages || [], {
          activePageId: space.activePageId || null,
          activeURL: space.lastURL || "",
        });
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
    await this.reapExpired();
    if (method === "health") return { ok: this.isHealthy(), service: "windows-browser-bridge", version: 1, reason: this.driver.unhealthyReason || null };
    if (method === "session.open") {
      if (this.stopping) throw new BridgeError("DAEMON_STOPPING", "browser bridge is stopping");
      if (sessionId) throw new BridgeError("SESSION_ID_FORBIDDEN", "session.open must not accept a client-supplied session id");
      const issuedSessionId = crypto.randomUUID();
      const timestamp = this.now();
      this.sessions.set(issuedSessionId, { createdAt: timestamp, lastSeen: timestamp, closing: false, inFlight: new Set(), closePromise: null });
      return { ok: true, sessionId: issuedSessionId };
    }
    if (method === "session.close") return this.closeSession(sessionId);
    if (method === "status") return { ok: this.isHealthy(), sessions: this.sessions.size, taskSpaces: (await this.store.load()).taskSpaces.length, browser: this.isHealthy(), reason: this.driver.unhealthyReason || null, persistenceError: this.lastReaperError };
    if (method === "shutdown") return { ok: true, shutdown: true, requestShutdown: true };
    return this.runSessionOperation(sessionId, async () => {
      if (method === "taskSpace.use" || method === "taskSpace.claim") return this.switchTaskSpace(sessionId, params);
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
      if (method === "page.publish") return this.driver.publishOnce(taskSpace.id, params.expectedLabel, params.authorized === true, taskSpace.platform);
      if (method === "page.inspect") return this.driver.inspectPublication(taskSpace.id, taskSpace.platform);
      if (method === "page.hover") return this.driver.hover(taskSpace.id, params.target, params.options || {});
      if (method === "page.press") return this.driver.pressKey(taskSpace.id, params.key);
      if (method === "page.upload") return this.driver.uploadFile(taskSpace.id, params.selector, params.path);
      if (method === "page.goto") { const result = await this.driver.gotoAndWait(taskSpace.id, params.url, params.options || {}); await this.persistPage(taskSpace); return result; }
      if (method === "wait") return this.driver.wait(params.seconds);
      throw new BridgeError("METHOD_NOT_FOUND", `unknown RPC method: ${method}`);
    });
  }

  async persistPage(taskSpace) {
    const tabs = await this.driver.listTabs(taskSpace.id);
    const activePageId = typeof this.driver.activePageId === "function" ? this.driver.activePageId(taskSpace.id) : null;
    taskSpace.lastURL = tabs.find(tab => tab.targetId === activePageId)?.url || taskSpace.lastURL;
    taskSpace.activePageId = activePageId;
    taskSpace.pages = tabs.map(tab => ({ logicalId: tab.targetId, lastURL: tab.url }));
    await this.store.update(taskSpace);
  }

  stop() {
    if (this.stopPromise) return this.stopPromise;
    this.stopping = true;
    if (this.reaper) clearInterval(this.reaper);
    this.stopPromise = (async () => {
      const results = [];
      const closing = [];
      for (const sessionId of [...this.sessions.keys()]) {
        try { closing.push(this.closeSession(sessionId, { reason: "stop", join: true })); }
        catch (error) { results.push({ ok: false, persisted: false, persistenceError: String(error?.message || error), reason: "stop" }); }
      }
      const closed = await Promise.allSettled(closing);
      for (const result of closed) {
        if (result.status === "fulfilled") results.push(result.value);
        else results.push({ ok: false, persisted: false, persistenceError: String(result.reason?.message || result.reason), reason: "stop" });
      }

      const driverResult = await this.withTimeout(
        Promise.resolve().then(() => this.driver.close?.()),
        this.operationDrainTimeoutMs,
        "browser driver close timed out",
      ).then(() => ({ ok: true }), error => ({ ok: false, error }));
      if (!driverResult.ok) results.push({ ok: false, persisted: null, persistenceError: String(driverResult.error?.message || driverResult.error), reason: "driver.close" });
      for (const [sessionId, session] of this.sessions) this.forceFinalizeSession(sessionId, session);
      this.sessions.clear();
      this.active.clear();
      this.switchQueues.clear();
      return { ok: results.every(result => result.ok) && !this.lastReaperError, sessions: results, persistenceError: this.lastReaperError };
    })();
    return this.stopPromise;
  }
}

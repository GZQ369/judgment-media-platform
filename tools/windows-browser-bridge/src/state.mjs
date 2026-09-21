import fs from "node:fs/promises";
import path from "node:path";
import { BridgeError } from "./errors.mjs";

const emptyState = () => ({ schemaVersion: 1, nextId: 1, taskSpaces: [] });

export async function ensureDir(dir) {
  await fs.mkdir(dir, { recursive: true });
}

async function readJSON(file) {
  return JSON.parse(await fs.readFile(file, "utf8"));
}

export class TaskSpaceStore {
  constructor(config) {
    this.config = config;
    this.state = emptyState();
    this.loaded = false;
    this.transactionQueue = Promise.resolve();
  }

  enqueue(task) {
    const next = this.transactionQueue.catch(() => {}).then(task);
    this.transactionQueue = next.catch(() => {});
    return next;
  }

  async loadUnsafe() {
    if (this.loaded) return this.state;
    await ensureDir(this.config.stateDir);
    try {
      this.state = await readJSON(this.config.stateFile);
      this.validate(this.state);
    } catch (error) {
      if (error.code !== "ENOENT") {
        try { await fs.copyFile(this.config.stateFile, `${this.config.stateFile}.corrupt-${Date.now()}.json`); } catch {}
      }
      try {
        const backup = await readJSON(this.config.backupFile);
        this.validate(backup);
        this.state = backup;
        await this.atomicWrite(this.config.stateFile, this.state);
      } catch {
        this.state = emptyState();
      }
    }
    this.loaded = true;
    return this.state;
  }

  async load() { return this.enqueue(() => this.loadUnsafe()); }

  validate(value) {
    if (!value || value.schemaVersion !== 1 || !Array.isArray(value.taskSpaces)) {
      throw new Error("invalid task space state");
    }
    for (const item of value.taskSpaces) {
      if (!Number.isInteger(item.id) || typeof item.name !== "string") throw new Error("invalid task space record");
    }
  }

  async saveUnsafe() {
      await ensureDir(this.config.stateDir);
      try {
        await fs.copyFile(this.config.stateFile, this.config.backupFile);
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      await this.atomicWrite(this.config.stateFile, this.state);
  }

  async save() { return this.enqueue(() => this.saveUnsafe()); }

  async atomicWrite(file, value) {
    const temp = `${file}.${process.pid}.${Date.now()}.tmp`;
    await fs.writeFile(temp, `${JSON.stringify(value, null, 2)}\n`, "utf8");
    await fs.rename(temp, file);
  }

  async find(ref) {
    return this.enqueue(async () => {
      await this.loadUnsafe();
      return this.findUnsafe(ref);
    });
  }

  findUnsafe(ref) {
    if (typeof ref === "number" || /^\d+$/.test(String(ref || ""))) {
      const id = Number(ref);
      const found = this.state.taskSpaces.find(item => item.id === id);
      if (!found) throw new BridgeError("TASK_SPACE_NOT_FOUND", `task space not found: ${id}`);
      return found;
    }
    return this.state.taskSpaces.find(item => item.name === String(ref)) || null;
  }

  async useOrCreate(ref, platform = "") {
    return this.enqueue(async () => {
      await this.loadUnsafe();
      const name = String(ref || "").trim();
      if (!name) throw new BridgeError("INVALID_TASK_SPACE", "task space name or numeric id is required");
      const existing = this.findUnsafe(name);
      if (existing) return existing;
      if (/^\d+$/.test(name)) throw new BridgeError("TASK_SPACE_NOT_FOUND", `task space not found: ${name}`);
      const item = {
        id: this.state.nextId++,
        name,
        platform: String(platform || ""),
        lastURL: "about:blank",
        pages: [],
        owner: null,
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      };
      this.state.taskSpaces.push(item);
      await this.saveUnsafe();
      return item;
    });
  }

  async update(item) {
    return this.enqueue(async () => {
      await this.loadUnsafe();
      const index = this.state.taskSpaces.findIndex(candidate => candidate.id === item.id);
      if (index < 0) throw new BridgeError("TASK_SPACE_NOT_FOUND", `task space not found: ${item.id}`);
      this.state.taskSpaces[index] = { ...item, updatedAt: new Date().toISOString() };
      await this.saveUnsafe();
    });
  }
}

export class LeaseManager {
  constructor({ ttlMs = 600000, now = () => Date.now() } = {}) { this.leases = new Map(); this.ttlMs = ttlMs; this.now = now; }

  reap(now = this.now()) {
    const expired = [];
    for (const [id, lease] of this.leases) {
      if (now - lease.lastSeen > this.ttlMs) { this.leases.delete(id); expired.push({ taskSpaceId: id, sessionId: lease.sessionId }); }
    }
    return expired;
  }

  acquire(taskSpaceId, sessionId) {
    const current = this.leases.get(taskSpaceId);
    if (current && current.sessionId !== sessionId) {
      throw new BridgeError("TASK_SPACE_BUSY", "user has taken control of this task space");
    }
    this.leases.set(taskSpaceId, { sessionId, lastSeen: this.now() });
    return { taskSpaceId, sessionId };
  }

  touch(sessionId) {
    const now = this.now();
    for (const lease of this.leases.values()) if (lease.sessionId === sessionId) lease.lastSeen = now;
  }

  releaseSession(sessionId) {
    for (const [id, lease] of this.leases) if (lease.sessionId === sessionId) this.leases.delete(id);
  }

  release(taskSpaceId, sessionId) {
    const lease = this.leases.get(taskSpaceId);
    if (lease?.sessionId === sessionId) this.leases.delete(taskSpaceId);
  }

  has(taskSpaceId, sessionId) { return this.leases.get(taskSpaceId)?.sessionId === sessionId; }

  owner(taskSpaceId) { return this.leases.get(taskSpaceId)?.sessionId || null; }
}

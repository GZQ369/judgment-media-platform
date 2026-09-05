import http from "node:http";
import crypto from "node:crypto";
import { asRPCError, BridgeError } from "./errors.mjs";

async function readBody(request, limit = 1024 * 1024) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > limit) throw new BridgeError("REQUEST_TOO_LARGE", "RPC request is too large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

export class RPCServer {
  constructor({ pipeName, token, bridge, onShutdown }) {
    this.pipeName = pipeName;
    this.token = token;
    this.bridge = bridge;
    this.onShutdown = onShutdown;
    this.server = null;
  }

  authorized(request) {
    const value = request.headers.authorization || "";
    return value === `Bearer ${this.token}`;
  }

  async handle(request) {
    if (request.method !== "POST" || request.url !== "/rpc") return { status: 404, body: { error: "not found" } };
    if (!this.authorized(request)) return { status: 401, body: { error: "unauthorized" } };
    const payload = JSON.parse(await readBody(request));
    if (!payload || typeof payload.method !== "string") throw new BridgeError("INVALID_RPC", "RPC method is required");
    const result = await this.bridge.dispatch(payload.method, payload.params || {}, payload.sessionId || "");
    if (result?.requestShutdown) setImmediate(() => this.stop().catch(() => {}));
    return { status: 200, body: { id: payload.id ?? null, result } };
  }

  async start() {
    this.server = http.createServer(async (request, response) => {
      try {
        const result = await this.handle(request);
        response.writeHead(result.status, { "content-type": "application/json; charset=utf-8" });
        response.end(JSON.stringify(result.body));
      } catch (error) {
        const rpcError = asRPCError(error);
        response.writeHead(error instanceof SyntaxError ? 400 : 500, { "content-type": "application/json; charset=utf-8" });
        response.end(JSON.stringify({ error: rpcError }));
      }
    });
    await new Promise((resolve, reject) => {
      this.server.once("error", reject);
      this.server.listen(this.pipeName, resolve);
    });
    return this.server.address();
  }

  async stop() {
    if (!this.server) return;
    await new Promise(resolve => this.server.close(resolve));
    this.server = null;
    await this.onShutdown?.();
  }
}

export function newSessionId() { return crypto.randomUUID(); }

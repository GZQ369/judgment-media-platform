import http from "node:http";
import crypto from "node:crypto";
import { getConfig } from "./config.mjs";

export class RPCClient {
  constructor(config = getConfig()) { this.config = config; this.sessionId = config.sessionId || crypto.randomUUID(); }

  request(method, params = {}, { timeout = this.config.requestTimeoutMs, sessionId = this.sessionId } = {}) {
    return new Promise((resolve, reject) => {
      const request = http.request({ socketPath: this.config.pipeName, path: "/rpc", method: "POST", timeout, headers: { authorization: `Bearer ${this.config.token}`, "content-type": "application/json" } }, response => {
        const chunks = [];
        response.on("data", chunk => chunks.push(chunk));
        response.on("end", () => {
          let body;
          try { body = JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch (error) { reject(error); return; }
          if (response.statusCode !== 200 || body.error) { const error = new Error(body.error?.message || body.error || `RPC status ${response.statusCode}`); error.code = body.error?.code || `HTTP_${response.statusCode}`; reject(error); return; }
          resolve(body.result);
        });
      });
      request.on("error", reject); request.on("timeout", () => request.destroy(new Error("bridge RPC timeout")));
      request.end(JSON.stringify({ id: crypto.randomUUID(), method, params, sessionId }));
    });
  }

  async openSession() {
    const result = await this.request("session.open", {}, { sessionId: "" });
    if (!result?.sessionId) throw new Error("daemon did not issue a session capability");
    this.sessionId = result.sessionId;
    return result;
  }

  closeSession() { return this.request("session.close"); }
}

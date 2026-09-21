import fs from "node:fs/promises";
import path from "node:path";
import { BridgeError } from "./errors.mjs";

export const ALLOWED_CDP_METHODS = new Set([
  "Runtime.evaluate",
  "DOM.setFileInputFiles",
  "Page.bringToFront",
  "Page.setWebLifecycleState",
  "Emulation.setFocusEmulationEnabled",
  "Input.insertText",
]);

export const FINAL_LABEL_RE = /^(发布|发布笔记|发表|立即投稿)$/;
const MEDIA_RE = /\.(mp4|m4v|mov|webm|mkv|avi|wmv|flv|ts|mpg|mpeg|rmvb|png|jpe?g|webp|gif|bmp)$/i;
const SENSITIVE_PART_RE = /(^|[\\/])(profile|state|credentials?|secrets?|cookies?|tokens?|auth)([\\/]|$)/i;

export function assertAllowedCDP(method) {
  if (!ALLOWED_CDP_METHODS.has(method)) throw new BridgeError("CDP_METHOD_NOT_ALLOWED", `CDP method is not allowed: ${method}`);
}

export async function validateUploadPath(inputPath, config, fsImpl = fs) {
  if (typeof inputPath !== "string" || !path.isAbsolute(inputPath)) throw new BridgeError("UPLOAD_PATH_INVALID", "upload path must be absolute");
  const resolved = path.resolve(inputPath);
  if (!MEDIA_RE.test(resolved)) throw new BridgeError("UPLOAD_TYPE_NOT_ALLOWED", "upload file extension is not allowed");
  const canonical = await fsImpl.realpath(resolved).catch(() => null);
  if (!canonical) throw new BridgeError("UPLOAD_PATH_INVALID", "upload path does not resolve to a file");
  if (SENSITIVE_PART_RE.test(canonical)) throw new BridgeError("UPLOAD_PATH_FORBIDDEN", "upload path targets a protected runtime directory");
  const stat = await fsImpl.stat(canonical).catch(() => null);
  if (!stat?.isFile()) throw new BridgeError("UPLOAD_PATH_INVALID", "upload path must be a regular file");
  if (config.allowedMediaRoot) {
    const root = await fsImpl.realpath(path.resolve(config.allowedMediaRoot)).catch(() => path.resolve(config.allowedMediaRoot));
    const relative = path.relative(root, canonical);
    if (relative.startsWith("..") || path.isAbsolute(relative)) throw new BridgeError("UPLOAD_PATH_FORBIDDEN", "upload path is outside WINDOWS_BROWSER_BRIDGE_MEDIA_ROOT");
  }
  return canonical;
}

export function normalizeLabel(value) { return String(value || "").replace(/\s+/g, " ").trim(); }

export function isFinalPublishLabel(value) { return FINAL_LABEL_RE.test(normalizeLabel(value)); }

export function guardSource() {
  return `(() => {
    const key = "__JUDGMENT_MEDIA_FINAL_PUBLISH_GUARD__";
    const finalText = /^(发布|发布笔记|发表|立即投稿)$/;
    const compact = value => String(value || "").replace(/\\s+/g, " ").trim();
    const label = element => {
      if (!(element instanceof Element)) return "";
      const text = compact(element.value || element.getAttribute("aria-label") || element.innerText || element.textContent);
      const buttonish = element.matches("button, input[type=submit], [role=button], .d-button, .bcc-button") || /button|btn|submit/i.test(String(element.className || ""));
      return buttonish ? text : "";
    };
    const state = window[key] || { armed: true, version: 2, blockedAttempts: [], authorizedAttempts: [] };
    const block = event => {
      const path = typeof event.composedPath === "function" ? event.composedPath() : [];
      const match = [event.submitter, ...path, event.target].find(element => finalText.test(label(element)));
      if (!match) return;
      const matchedLabel = label(match);
      const authorization = state.authorization;
      if (authorization && authorization.label === matchedLabel && Date.now() <= authorization.expiresAt) {
        state.authorizedAttempts.push({ type: event.type, label: matchedLabel, at: new Date().toISOString() });
        return;
      }
      event.preventDefault(); event.stopImmediatePropagation();
      state.blockedAttempts.push({ type: event.type, label: matchedLabel, at: new Date().toISOString() });
    };
    if (!state.listenersInstalled) {
      document.addEventListener("click", block, true);
      document.addEventListener("submit", block, true);
      state.listenersInstalled = true;
    }
    state.armed = true; state.version = 2; state.authorization = null; window[key] = state;
    return { ok: true, armed: true, version: 2, blockedAttempts: state.blockedAttempts.length };
  })()`;
}

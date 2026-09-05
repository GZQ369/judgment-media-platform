import assert from "node:assert/strict";
import test from "node:test";
import vm from "node:vm";
import { guardSource, isFinalPublishLabel } from "../src/security.mjs";

class Element {
  constructor(text, button = false) { this.innerText = text; this.textContent = text; this.button = button; }
  matches() { return this.button; }
  getAttribute() { return null; }
}

test("selector, point and focused child checks inspect every ancestor", () => {
  assert.equal(["child", "发布"].some(isFinalPublishLabel), true);
  assert.equal(["child", "立即投稿"].some(isFinalPublishLabel), true);
  assert.equal(["child", "安全"].some(isFinalPublishLabel), false);
});

test("page capture guard blocks DOM click and submit events", () => {
  const listeners = {};
  const document = { addEventListener: (type, fn, capture) => { listeners[type] = { fn, capture }; } };
  const window = {};
  vm.runInNewContext(guardSource(), { window, document, Element, Date });
  assert.equal(listeners.click.capture, true); assert.equal(listeners.submit.capture, true);
  const button = new Element("发布", true);
  for (const type of ["click", "submit"]) {
    let prevented = false; let stopped = false;
    listeners[type].fn({ target: button, submitter: type === "submit" ? button : undefined, composedPath: () => [button], preventDefault: () => { prevented = true; }, stopImmediatePropagation: () => { stopped = true; } });
    assert.equal(prevented, true); assert.equal(stopped, true);
  }
  assert.equal(window.__JUDGMENT_MEDIA_FINAL_PUBLISH_GUARD__.blockedAttempts.length, 2);
});

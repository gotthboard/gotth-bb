import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { auditAccessibility, auditReflow } from "./browser-accessibility.mjs";

const chromium = process.env.CHROMIUM || "/usr/bin/chromium";
const target = process.env.GOTTH_BB_UNREAD_BROWSER_URL;
assert(target, "GOTTH_BB_UNREAD_BROWSER_URL is required");

function devtools(webSocket) {
  let nextID = 1;
  const pending = new Map();
  webSocket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (!message.id || !pending.has(message.id)) return;
    const { resolve, reject } = pending.get(message.id);
    pending.delete(message.id);
    if (message.error) reject(new Error(message.error.message));
    else resolve(message.result);
  });
  return (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = nextID++;
    pending.set(id, { resolve, reject });
    webSocket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
}

async function evaluate(send, sessionId, expression) {
  const result = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true }, sessionId);
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
  return result.result.value;
}

async function waitFor(send, sessionId, expression) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (await evaluate(send, sessionId, expression)) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`browser condition timed out: ${expression}`);
}

async function tabTo(send, sessionId, text) {
  for (let attempt = 0; attempt < 40; attempt++) {
    await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
    await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
    const active = await evaluate(send, sessionId, "document.activeElement?.textContent?.trim() || ''");
    if (active === text) return;
  }
  throw new Error(`keyboard focus did not reach ${text}`);
}

async function pressEnter(send, sessionId) {
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", text: "\r", unmodifiedText: "\r", windowsVirtualKeyCode: 13 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, sessionId);
}

test("unread controls remain keyboard operable without JavaScript", async (t) => {
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-unread-chromium-"));
  const browser = spawn(chromium, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-networking",
    "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  t.after(async () => {
    browser.kill("SIGTERM");
    if (browser.exitCode === null) await new Promise((resolve) => browser.once("exit", resolve));
    await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  });

  const endpoint = await new Promise((resolve, reject) => {
    let stderr = "";
    const timeout = setTimeout(() => reject(new Error("Chromium DevTools endpoint timed out")), 10_000);
    browser.stderr.on("data", (chunk) => {
      stderr += chunk;
      const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (!match) return;
      clearTimeout(timeout);
      resolve(match[1]);
    });
    browser.once("exit", (code) => reject(new Error(`Chromium exited before startup (${code}): ${stderr}`)));
  });

  const webSocket = new WebSocket(endpoint);
  await new Promise((resolve, reject) => {
    webSocket.addEventListener("open", resolve, { once: true });
    webSocket.addEventListener("error", reject, { once: true });
  });
  t.after(() => webSocket.close());
  const send = devtools(webSocket);
  const { targetId } = await send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  await send("Page.enable", {}, sessionId);
  await send("Runtime.enable", {}, sessionId);
  await send("Accessibility.enable", {}, sessionId);
  await send("Emulation.setScriptExecutionDisabled", { value: true }, sessionId);
  await send("Page.navigate", { url: target }, sessionId);
  await waitFor(send, sessionId, "document.readyState === 'complete' && !!document.querySelector('form[action$=\"/read\"]')");

  const accessibility = await send("Accessibility.getFullAXTree", {}, sessionId);
  const namedRoles = accessibility.nodes.map((node) => `${node.role?.value || ''}:${node.name?.value || ''}`);
  assert(namedRoles.includes("link:Jump to first unread post"));
  assert(namedRoles.includes("button:Mark topic read"));
  assert.equal(await evaluate(send, sessionId, "document.querySelector('script') !== null"), true);
  await auditAccessibility(send, sessionId, evaluate, "topic unread controls without JavaScript");
  await auditReflow(send, sessionId, evaluate, "topic unread controls without JavaScript");

  await tabTo(send, sessionId, "Jump to first unread post");
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "location.hash === '#post-101' && !!document.querySelector('#post-101')");

  await send("Page.navigate", { url: target }, sessionId);
  await waitFor(send, sessionId, "document.readyState === 'complete' && !!document.querySelector('form[action$=\"/read\"]')");
  await tabTo(send, sessionId, "Mark topic read");
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "document.readyState === 'complete' && document.body.textContent.includes('Read') && !document.querySelector('form[action$=\"/read\"]')");
  assert.equal(await evaluate(send, sessionId, "document.querySelector('a[href$=\"/unread\"]')"), null);
});

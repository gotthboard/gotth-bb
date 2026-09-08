import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { auditReflow } from "./browser-accessibility.mjs";

const chromium = process.env.CHROMIUM || "/usr/bin/chromium";
const target = process.env.GOTTH_BB_DISCOVERY_REFLOW_URL;
assert(target, "GOTTH_BB_DISCOVERY_REFLOW_URL is required");

function devtools(webSocket) {
  let nextID = 1;
  const pending = new Map();
  webSocket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (!message.id || !pending.has(message.id)) return;
    const pair = pending.get(message.id);
    pending.delete(message.id);
    message.error ? pair.reject(new Error(message.error.message)) : pair.resolve(message.result);
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
    try {
      if (await evaluate(send, sessionId, expression)) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`browser condition timed out: ${expression}`);
}

test("search remains usable at 320 CSS pixels and 200% zoom", async (t) => {
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-discovery-reflow-"));
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
      if (match) { clearTimeout(timeout); resolve(match[1]); }
    });
    browser.once("exit", (code) => reject(new Error(`Chromium exited before startup (${code})`)));
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
  await send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true }, sessionId);
  await send("Page.navigate", { url: target }, sessionId);
  await waitFor(send, sessionId, "document.readyState === 'complete' && document.querySelector('input[name=\"from\"]') && document.querySelector('input[name=\"to\"]') && getComputedStyle(document.querySelector('input[name=\"from\"]').closest('label').parentElement).display === 'grid'");
  const filterRows = await evaluate(send, sessionId, `(() => {
    const fromLabel = document.querySelector('input[name="from"]').closest('label');
    const toLabel = document.querySelector('input[name="to"]').closest('label');
    const from = fromLabel.getBoundingClientRect();
    const to = toLabel.getBoundingClientRect();
    return {
      fromBottom: from.bottom,
      toTop: to.top,
      innerWidth,
      clientWidth: document.documentElement.clientWidth,
      wideMedia: matchMedia('(min-width: 40rem)').matches,
      columns: getComputedStyle(fromLabel.parentElement).gridTemplateColumns,
      wrapperClass: fromLabel.parentElement.className,
    };
  })()`);
  assert.ok(filterRows.toTop >= filterRows.fromBottom, `search date filters do not stack at 320 CSS pixels: ${JSON.stringify(filterRows)}`);
  console.log(`REFLOW label="search date filter rows" width=320 stacked=true fromBottom=${filterRows.fromBottom} toTop=${filterRows.toTop}`);
  await evaluate(send, sessionId, "document.body.style.minHeight = '200vh'; true");
  await auditReflow(send, sessionId, evaluate, "search filters");
});

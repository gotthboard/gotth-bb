import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const chromium = process.env.CHROMIUM || "/usr/bin/chromium";
const root = process.env.GOTTH_BB_ABUSE_BROWSER_URL;
assert(root, "GOTTH_BB_ABUSE_BROWSER_URL is required");

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
  for (let attempt = 0; attempt < 120; attempt++) {
    try {
      if (await evaluate(send, sessionId, expression)) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  const state = await evaluate(send, sessionId, `({ href: location.href, body: document.body?.textContent?.replace(/\\s+/g, " ").trim().slice(0, 400) })`);
  throw new Error(`browser condition timed out: ${expression}; state=${JSON.stringify(state)}`);
}

async function navigate(send, sessionId, url, condition) {
  await send("Page.navigate", { url }, sessionId);
  await waitFor(send, sessionId, `document.readyState === "complete" && (${condition})`);
}

async function submit(send, sessionId, selector, values) {
  assert.equal(await evaluate(send, sessionId, `(() => {
    const form = document.querySelector(${JSON.stringify(selector)});
    if (!form) throw new Error("form missing");
    for (const [name, value] of Object.entries(${JSON.stringify(values)})) {
      const field = form.elements.namedItem(name);
      if (!field) throw new Error("field missing: " + name);
      field.value = value;
    }
    form.requestSubmit();
    return true;
  })()`), true);
}

test("abuse rejections preserve ordinary forms without JavaScript", async (t) => {
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-abuse-chromium-"));
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
  await send("Emulation.setScriptExecutionDisabled", { value: true }, sessionId);

  const blocked = "private <sentinel> [link](https://blocked.example/path)";
  await navigate(send, sessionId, `${root}/topics/new?area=news`, `!!document.querySelector('form[action$="/topics"]')`);
  assert.equal(await evaluate(send, sessionId, "typeof htmx"), "undefined");
  await submit(send, sessionId, `form[action$="/topics"]`, { title: "Blocked", markdown: blocked });
  await waitFor(send, sessionId, `document.body.textContent.includes("This draft contains a blocked link")`);
  assert.equal(await evaluate(send, sessionId, `document.querySelector('textarea[name="markdown"]').value`), blocked);

  await submit(send, sessionId, `form[action$="/topics"]`, { title: "Rate", markdown: "retained rate draft" });
  await waitFor(send, sessionId, `document.body.textContent.includes("Please wait before publishing again")`);
  assert.equal(await evaluate(send, sessionId, `document.querySelector('textarea[name="markdown"]').value`), "retained rate draft");

  await navigate(send, sessionId, `${root}/posts/91/edit`, `!!document.querySelector('form[action$="/posts/91/edit"]')`);
  await submit(send, sessionId, `form[action$="/posts/91/edit"]`, { markdown: blocked });
  await waitFor(send, sessionId, `document.body.textContent.includes("This draft contains a blocked link")`);
  assert.equal(await evaluate(send, sessionId, `document.querySelector('textarea[name="markdown"]').value`), blocked);

  await navigate(send, sessionId, `${root}/admin/settings`, `!!document.querySelector('form[action$="/admin/settings"]')`);
  await submit(send, sessionId, `form[action$="/admin/settings"]`, {
    site_name: "Submitted browser board", site_description: "Submitted browser description", brand_theme: "rose",
    rules_markdown: blocked, reason: "Retained browser reason",
  });
  await waitFor(send, sessionId, `document.body.textContent.includes("This draft contains a blocked link")`);
  assert.equal(await evaluate(send, sessionId, `document.querySelector('textarea[name="rules_markdown"]').value`), blocked);
  assert.equal(await evaluate(send, sessionId, `document.querySelector('input[name="reason"]').value`), "Retained browser reason");
  assert.equal(await evaluate(send, sessionId, `document.querySelector('input[name="revision"]').value`), "9");
});

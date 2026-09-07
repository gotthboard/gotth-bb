import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const toolbarScript = readFileSync(new URL("./markdown-toolbar.js", import.meta.url), "utf8");
const chromium = process.env.CHROMIUM || "/usr/bin/chromium";

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

test("Chromium preserves live IME state across physical pointer activation", async (t) => {
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-toolbar-chromium-"));
  const browser = spawn(chromium, [
    "--headless=new",
    "--no-sandbox",
    "--disable-gpu",
    "--disable-background-networking",
    "--remote-debugging-port=0",
    `--user-data-dir=${profile}`,
    "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  t.after(async () => {
    browser.kill("SIGTERM");
    await new Promise((resolve) => browser.once("exit", resolve));
    await rm(profile, { recursive: true, force: true });
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

  const html = `<!doctype html><meta charset="utf-8"><style>body{min-height:800px}</style>
    <div data-markdown-editor>
      <textarea id="draft">text</textarea>
      <div data-markdown-toolbar hidden><button id="bold" type="button" data-markdown-action="bold">Bold</button></div>
    </div>
    <script>
      globalThis.toolbarEvents = [];
      for (const type of ["compositionstart", "compositionupdate", "compositionend", "beforeinput", "input", "blur", "focus"])
        draft.addEventListener(type, () => toolbarEvents.push("t:" + type));
      for (const type of ["pointerdown", "mousedown", "mouseup", "click", "blur", "focus"])
        bold.addEventListener(type, () => toolbarEvents.push("b:" + type));
    <\/script>
    <script>${toolbarScript.replaceAll("</script", "<\\/script")}<\/script>`;
  const url = `data:text/html;base64,${Buffer.from(html).toString("base64")}`;
  await send("Page.navigate", { url }, sessionId);
  await evaluate(send, sessionId, "new Promise(resolve => document.readyState === 'complete' ? resolve() : addEventListener('load', resolve, {once:true}))");
  await evaluate(send, sessionId, "draft.focus(); draft.setSelectionRange(0, 0)");
  await send("Input.imeSetComposition", { text: "正在", selectionStart: 2, selectionEnd: 2 }, sessionId);

  const before = await evaluate(send, sessionId, `({
    value: draft.value,
    start: draft.selectionStart,
    end: draft.selectionEnd,
    active: document.activeElement.id,
    rect: (() => { const r = bold.getBoundingClientRect(); return {x: r.x + r.width / 2, y: r.y + r.height / 2}; })()
  })`);
  await send("Input.dispatchMouseEvent", { type: "mousePressed", x: before.rect.x, y: before.rect.y, button: "left", clickCount: 1 }, sessionId);
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: before.rect.x, y: before.rect.y, button: "left", clickCount: 1 }, sessionId);
  const after = await evaluate(send, sessionId, `({
    value: draft.value,
    start: draft.selectionStart,
    end: draft.selectionEnd,
    active: document.activeElement.id,
    events: toolbarEvents
  })`);
  assert.deepEqual(
    { value: after.value, start: after.start, end: after.end, active: after.active },
    { value: before.value, start: before.start, end: before.end, active: before.active },
  );
  assert(after.events.includes("b:pointerdown"));
  assert(after.events.includes("b:click"));
  assert(!after.events.includes("t:compositionend"));
  assert(!after.events.includes("t:blur"));

  // Finish the first composition, reset, and begin another gesture that is
  // physically released away from the button. It must generate no button
  // click and must retire its guard before later native keyboard activation.
  await send("Input.insertText", { text: "正在" }, sessionId);
  await evaluate(send, sessionId, "draft.value = 'text'; draft.focus(); draft.setSelectionRange(0, 0); toolbarEvents = []");
  await send("Input.imeSetComposition", { text: "正在", selectionStart: 2, selectionEnd: 2 }, sessionId);
  const offButton = await evaluate(send, sessionId, `({
    value: draft.value,
    start: draft.selectionStart,
    end: draft.selectionEnd,
    active: document.activeElement.id,
    rect: (() => { const r = bold.getBoundingClientRect(); return {x: r.x + r.width / 2, y: r.y + r.height / 2}; })()
  })`);
  await send("Input.dispatchMouseEvent", { type: "mousePressed", x: offButton.rect.x, y: offButton.rect.y, button: "left", clickCount: 1 }, sessionId);
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 500, y: 500, button: "none" }, sessionId);
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: 500, y: 500, button: "left", clickCount: 1 }, sessionId);
  await evaluate(send, sessionId, "new Promise(resolve => setTimeout(resolve, 0))");
  const releasedAway = await evaluate(send, sessionId, "({value: draft.value, start: draft.selectionStart, end: draft.selectionEnd, active: document.activeElement.id, events: toolbarEvents})");
  assert.deepEqual(
    { value: releasedAway.value, start: releasedAway.start, end: releasedAway.end, active: releasedAway.active },
    { value: offButton.value, start: offButton.start, end: offButton.end, active: offButton.active },
  );
  assert(releasedAway.events.includes("b:pointerdown"));
  assert(!releasedAway.events.includes("b:click"));
  assert(!releasedAway.events.includes("t:compositionend"));
  assert(!releasedAway.events.includes("t:blur"));

  // Commit the browser-owned composition, then use native keyboard navigation
  // with both Space and Enter. No custom key handling is involved.
  await send("Input.insertText", { text: "正在" }, sessionId);
  await evaluate(send, sessionId, "draft.setSelectionRange(2, draft.value.length)");
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
  assert.equal(await evaluate(send, sessionId, "document.activeElement.id"), "bold");
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: " ", code: "Space", windowsVirtualKeyCode: 32 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: " ", code: "Space", windowsVirtualKeyCode: 32 }, sessionId);
  const keyboard = await evaluate(send, sessionId, "({value: draft.value, active: document.activeElement.id})");
  assert.equal(keyboard.value, "正在**text**");
  assert.equal(keyboard.active, "draft");

  await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
  assert.equal(await evaluate(send, sessionId, "document.activeElement.id"), "bold");
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", text: "\r", unmodifiedText: "\r", windowsVirtualKeyCode: 13 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, sessionId);
  const enter = await evaluate(send, sessionId, "({value: draft.value, active: document.activeElement.id})");
  assert.equal(enter.value, "正在text");
  assert.equal(enter.active, "draft");
});

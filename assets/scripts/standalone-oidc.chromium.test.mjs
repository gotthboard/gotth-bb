import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const chromium = process.env.CHROMIUM || "/usr/bin/chromium";
const loginURL = process.env.GOTTH_BB_STANDALONE_LOGIN_URL;
const callbackOrigin = process.env.GOTTH_BB_STANDALONE_CALLBACK_ORIGIN;
const directOrigin = process.env.GOTTH_BB_STANDALONE_DIRECT_ORIGIN;
const passwordFile = process.env.GOTTH_BB_STANDALONE_PASSWORD_FILE;
const username = process.env.GOTTH_BB_STANDALONE_USERNAME;
for (const [name, value] of Object.entries({ loginURL, callbackOrigin, directOrigin, passwordFile, username })) {
  assert(value, `${name} is required`);
}

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

async function waitFor(send, sessionId, expression, attempts = 240) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    try {
      if (await evaluate(send, sessionId, expression)) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`browser condition timed out: ${expression}`);
}

const deepHelpers = `
  const deepElements = () => {
    const roots = [document];
    const elements = [];
    for (let index = 0; index < roots.length; index++) {
      for (const element of roots[index].querySelectorAll('*')) {
        elements.push(element);
        if (element.shadowRoot) roots.push(element.shadowRoot);
      }
    }
    return elements;
  };
  const visible = (element) => {
    const box = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return box.width > 0 && box.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
  };
`;

async function visibleInputs(send, sessionId) {
  return evaluate(send, sessionId, `(() => { ${deepHelpers}
    return deepElements().filter((element) => element instanceof HTMLInputElement && visible(element)).map((element) => ({
      type: element.type, name: element.name, autocomplete: element.autocomplete, placeholder: element.placeholder,
    }));
  })()`);
}

async function visibleButtons(send, sessionId) {
  return evaluate(send, sessionId, `(() => { ${deepHelpers}
    return deepElements().filter((element) => element instanceof HTMLButtonElement && visible(element)).map((element) => ({
      type: element.type, name: element.name, text: (element.textContent || '').trim(), disabled: element.disabled,
    }));
  })()`);
}

async function fill(send, sessionId, selectorExpression, value) {
  return evaluate(send, sessionId, `(() => { ${deepHelpers}
    const input = deepElements().find((element) => element instanceof HTMLInputElement && visible(element) && (${selectorExpression}));
    if (!input) return false;
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
    setter.call(input, ${JSON.stringify(value)});
    input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: null }));
    input.dispatchEvent(new Event('change', { bubbles: true }));
    return true;
  })()`);
}

async function submit(send, sessionId) {
  return evaluate(send, sessionId, `(() => { ${deepHelpers}
    const button = deepElements().find((element) =>
      element instanceof HTMLButtonElement && visible(element) && !element.disabled &&
      (element.type === 'submit' || /log in|continue|sign in/i.test(element.textContent || ''))
    );
    if (!button) return false;
    button.click();
    return true;
  })()`);
}

test("dedicated Authentik completes one Board OIDC login", async (t) => {
  const passwordBuffer = await readFile(passwordFile);
  const password = passwordBuffer.toString("utf8");
  assert(password.length > 0 && !/[\0\r\n]/u.test(password), "password file framing is invalid");
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-standalone-oidc-"));
  const browser = spawn(chromium, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-networking",
    "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  t.after(async () => {
    passwordBuffer.fill(0);
    browser.kill("SIGTERM");
    if (browser.exitCode === null) await new Promise((resolve) => browser.once("exit", resolve));
    await rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 });
  });
  const endpoint = await new Promise((resolve, reject) => {
    let stderr = "";
    const timeout = setTimeout(() => reject(new Error("Chromium DevTools endpoint timed out")), 10_000);
    browser.stderr.on("data", (chunk) => {
      stderr += chunk;
      const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/u);
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
  await send("Network.enable", {}, sessionId);
  await send("Page.navigate", { url: loginURL }, sessionId);
  await waitFor(send, sessionId, `location.href.startsWith(${JSON.stringify(new URL(loginURL).origin)}) === false && document.readyState === 'complete'`);

  await waitFor(send, sessionId, `(() => { ${deepHelpers} return deepElements().some((element) => element instanceof HTMLInputElement && visible(element)); })()`);
  let inputs = await visibleInputs(send, sessionId);
  console.log(`OIDC_STAGE inputs=${JSON.stringify(inputs)} buttons=${JSON.stringify(await visibleButtons(send, sessionId))}`);
  const userFilled = await fill(send, sessionId, "element.name === 'uidField'", username);
  assert(userFilled, "visible Authentik username input is unavailable");
  assert(await submit(send, sessionId), "visible Authentik submit button is unavailable");

  await waitFor(send, sessionId, `location.href.startsWith(${JSON.stringify(callbackOrigin)}) || (() => { ${deepHelpers} return !deepElements().some((element) => element instanceof HTMLInputElement && element.name === 'uidField' && visible(element)); })()`);
  if (!(await evaluate(send, sessionId, `location.href.startsWith(${JSON.stringify(callbackOrigin)})`))) {
    inputs = await visibleInputs(send, sessionId);
    console.log(`OIDC_STAGE inputs=${JSON.stringify(inputs)} buttons=${JSON.stringify(await visibleButtons(send, sessionId))}`);
    assert(await fill(send, sessionId, "element.type === 'password'", password), "second-stage Authentik password input is unavailable");
    assert(await submit(send, sessionId), "second-stage Authentik submit button is unavailable");
    await waitFor(send, sessionId, `location.href.startsWith(${JSON.stringify(callbackOrigin)})`);
  }
  const callback = await evaluate(send, sessionId, "location.href");
  assert(callback.startsWith(callbackOrigin), "Authentik did not return to the exact Board callback origin");
  const directCallback = directOrigin + new URL(callback).pathname + new URL(callback).search;
  await send("Page.navigate", { url: directCallback }, sessionId);
  await waitFor(send, sessionId, `location.origin === ${JSON.stringify(directOrigin)} && document.readyState === 'complete' && !location.pathname.startsWith('/auth/callback')`);
  const cookies = await send("Network.getAllCookies", {}, sessionId);
  assert(cookies.cookies.some((cookie) => cookie.name === "gotth_bb_session"), "Board session cookie is absent after OIDC callback");
  console.log("OIDC_LOGIN callback_replayed_direct=true session_cookie=true");
});

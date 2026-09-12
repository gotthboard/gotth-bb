import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { auditAccessibility, auditReflow } from "./browser-accessibility.mjs";

const chromium = process.env.CHROMIUM || "/usr/bin/chromium";
const target = process.env.GOTTH_BB_ADMINISTRATION_BROWSER_URL;
assert(target, "GOTTH_BB_ADMINISTRATION_BROWSER_URL is required");

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
  let lastError;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      if (await evaluate(send, sessionId, expression)) return;
      lastError = undefined;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  const detail = lastError ? `; last evaluation failed: ${lastError.message}` : "";
  let state = "";
  try {
    state = JSON.stringify(await evaluate(send, sessionId, `({ href: location.href, body: document.body?.textContent?.replace(/\\s+/g, " ").trim().slice(0, 500) })`));
  } catch (error) {
    state = `unavailable: ${error.message}`;
  }
  throw new Error(`browser condition timed out: ${expression}${detail}; state=${state}`);
}

async function tabTo(send, sessionId, expression, label) {
  for (let attempt = 0; attempt < 80; attempt++) {
    await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
    await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, sessionId);
    if (await evaluate(send, sessionId, expression)) return;
  }
  throw new Error(`keyboard focus did not reach ${label}`);
}

async function pressEnter(send, sessionId) {
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", text: "\r", unmodifiedText: "\r", windowsVirtualKeyCode: 13 }, sessionId);
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, sessionId);
}

async function assertVisibleFocus(send, sessionId) {
  const focus = await evaluate(send, sessionId, `(() => {
    const style = getComputedStyle(document.activeElement);
    return { style: style.outlineStyle, width: style.outlineWidth };
  })()`);
  assert.notEqual(focus.style, "none");
  assert.notEqual(focus.width, "0px");
}

async function navigate(send, sessionId, url, condition) {
  await send("Page.navigate", { url }, sessionId);
  await waitFor(send, sessionId, `document.readyState === 'complete' && (${condition})`);
}

async function auditAreaFormPresentation(send, sessionId, label, formSelector, gridSelectors) {
  await send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  const narrow = await evaluate(send, sessionId, `(() => {
    const form = document.querySelector(${JSON.stringify(formSelector)});
    if (!form) throw new Error("area form is absent");
    const controls = [...form.querySelectorAll("input:not([type=hidden]), textarea, select, button")];
    const formBackground = getComputedStyle(form).backgroundColor;
    return {
      viewport: document.documentElement.clientWidth,
      pageWidth: document.documentElement.scrollWidth,
      controls: controls.map((control) => {
        const style = getComputedStyle(control);
        const rect = control.getBoundingClientRect();
        return {
          name: control.name || control.textContent.trim(),
          tag: control.tagName,
          left: rect.left,
          right: rect.right,
          width: rect.width,
          height: rect.height,
          border: style.borderTopWidth,
          background: style.backgroundColor,
          formBackground,
        };
      }),
      columns: ${JSON.stringify(gridSelectors)}.map((selector) => getComputedStyle(document.querySelector(selector)).gridTemplateColumns.split(" ").length),
    };
  })()`);
  assert.ok(narrow.pageWidth <= narrow.viewport, `${label} overflows at 320px: ${JSON.stringify(narrow)}`);
  assert.ok(narrow.controls.length >= 7, `${label} has too few visible controls: ${JSON.stringify(narrow.controls)}`);
  for (const control of narrow.controls) {
    assert.ok(control.left >= 0 && control.right <= narrow.viewport, `${label} control escapes viewport: ${JSON.stringify(control)}`);
    assert.ok(control.width > 80 && control.height >= 44, `${label} control is not usable: ${JSON.stringify(control)}`);
    if (control.tag !== "BUTTON") assert.notEqual(control.border, "0px", `${label} control has no visible border: ${JSON.stringify(control)}`);
    assert.notEqual(control.background, control.formBackground, `${label} control disappears into form background: ${JSON.stringify(control)}`);
  }
  assert.deepEqual(narrow.columns, gridSelectors.map(() => 1), `${label} grids do not stack at 320px`);

  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId);
  const desktopColumns = await evaluate(send, sessionId, `${JSON.stringify(gridSelectors)}.map((selector) => getComputedStyle(document.querySelector(selector)).gridTemplateColumns.split(" ").length)`);
  assert.deepEqual(desktopColumns, gridSelectors.map((selector) => selector.includes("identity") ? 2 : 3), `${label} desktop grids are not bounded: ${JSON.stringify(desktopColumns)}`);
  console.log(`AREA_UI label=${JSON.stringify(label)} mobile_controls=${narrow.controls.length} mobile_columns=${JSON.stringify(narrow.columns)} desktop_columns=${JSON.stringify(desktopColumns)} result=pass`);
}

async function auditAreaGroupActionPresentation(send, sessionId, label) {
  await send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  const narrow = await evaluate(send, sessionId, `(() => ({
    viewport: document.documentElement.clientWidth,
    pageWidth: document.documentElement.scrollWidth,
    forms: [...document.querySelectorAll("[data-area-group-action]")].map((form) => ({
      columns: getComputedStyle(form).gridTemplateColumns.split(" ").length,
      controls: [...form.querySelectorAll("input:not([type=hidden]), button")].map((control) => {
        const style = getComputedStyle(control);
        const rect = control.getBoundingClientRect();
        return { name: control.name || control.textContent.trim(), left: rect.left, right: rect.right, width: rect.width, height: rect.height, border: style.borderTopWidth };
      }),
    })),
  }))()`);
  assert.ok(narrow.pageWidth <= narrow.viewport, `${label} overflows at 320px: ${JSON.stringify(narrow)}`);
  assert.ok(narrow.forms.length > 0, `${label} has no group action forms`);
  for (const form of narrow.forms) {
    assert.equal(form.columns, 1, `${label} group action does not stack at 320px: ${JSON.stringify(form)}`);
    assert.equal(form.controls.length, 2, `${label} group action controls are incomplete: ${JSON.stringify(form)}`);
    for (const control of form.controls) {
      assert.ok(control.left >= 0 && control.right <= narrow.viewport, `${label} group control escapes viewport: ${JSON.stringify(control)}`);
      assert.ok(control.width > 80 && control.height >= 44, `${label} group control is not usable: ${JSON.stringify(control)}`);
      assert.notEqual(control.border, "0px", `${label} group control has no visible border: ${JSON.stringify(control)}`);
    }
  }

  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId);
  const desktopColumns = await evaluate(send, sessionId, `[...document.querySelectorAll("[data-area-group-action]")].map((form) => getComputedStyle(form).gridTemplateColumns.split(" ").length)`);
  assert.ok(desktopColumns.every((columns) => columns === 2), `${label} desktop group actions are not bounded: ${JSON.stringify(desktopColumns)}`);
  console.log(`AREA_GROUP_UI label=${JSON.stringify(label)} mobile_forms=${narrow.forms.length} desktop_columns=${JSON.stringify(desktopColumns)} result=pass`);
}

async function submitForm(send, sessionId, selector, values) {
  const expression = `(() => {
    const form = document.querySelector(${JSON.stringify(selector)});
    if (!form) throw new Error("form not found: " + ${JSON.stringify(selector)});
    for (const [name, value] of Object.entries(${JSON.stringify(values)})) {
      const field = form.elements.namedItem(name);
      if (!field) throw new Error("field not found: " + name);
      field.value = value;
      field.dispatchEvent(new Event("change", { bubbles: true }));
    }
    if (!form.checkValidity()) {
      const invalid = [...form.elements].filter((field) => field.willValidate && !field.validity.valid).map((field) => ({ name: field.name, value: field.value, message: field.validationMessage }));
      throw new Error("form is invalid: " + JSON.stringify(invalid));
    }
    form.requestSubmit();
    return true;
  })()`;
  assert.equal(await evaluate(send, sessionId, expression), true);
}

test("administration remains keyboard operable without JavaScript", async (t) => {
  const profile = await mkdtemp(join(tmpdir(), "gotth-bb-administration-chromium-"));
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
  await waitFor(send, sessionId, "document.readyState === 'complete' && document.body.textContent.includes('Board administration')");

  let accessibility = await send("Accessibility.getFullAXTree", {}, sessionId);
  let namedRoles = accessibility.nodes.map((node) => `${node.role?.value || ''}:${String(node.name?.value || '').replace(/\s+/g, ' ').trim()}`);
  for (const expected of ["heading:Board administration", "link:Overview", "link:Accounts", "link:Groups", "link:Areas", "link:Control", "link:Email", "link:Settings"]) {
    assert(namedRoles.includes(expected), `accessibility tree lacks ${expected}`);
  }
  assert.equal(await evaluate(send, sessionId, "document.querySelector('script') !== null"), true);
  await auditReflow(send, sessionId, evaluate, "administration dashboard");

  await tabTo(send, sessionId, "document.activeElement?.textContent?.trim() === 'Accounts' && document.activeElement?.closest('nav')?.getAttribute('aria-label') === 'Administration'", "administration Accounts link");
  await assertVisibleFocus(send, sessionId);
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/accounts') && document.body.textContent.includes('Local Member')");

  await tabTo(send, sessionId, "document.activeElement?.textContent?.trim() === 'Local Member'", "account detail link");
  await assertVisibleFocus(send, sessionId);
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('Change role')");

  accessibility = await send("Accessibility.getFullAXTree", {}, sessionId);
  namedRoles = accessibility.nodes.map((node) => `${node.role?.value || ''}:${String(node.name?.value || '').replace(/\s+/g, ' ').trim()}`);
  for (const expected of ["heading:Local Member", "combobox:Role", "textbox:Audit reason", "button:Retry identity reconciliation", "button:Change role", "button:Revoke"]) {
    assert(namedRoles.includes(expected), `account accessibility tree lacks ${expected}`);
  }

  await tabTo(send, sessionId, "document.activeElement?.matches('form[action$=\"/identity/reconcile\"] input[name=\"reason\"]')", "identity reconciliation audit reason");
  await assertVisibleFocus(send, sessionId);
  await send("Input.insertText", { text: "Retry browser identity" }, sessionId);
  await tabTo(send, sessionId, "document.activeElement?.textContent?.trim() === 'Retry identity reconciliation'", "Retry identity reconciliation button");
  await assertVisibleFocus(send, sessionId);
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('identity accepted') && !document.querySelector('form[action$=\"/identity/reconcile\"]')");

  await tabTo(send, sessionId, "document.activeElement?.matches('form[action$=\"/role\"] input[name=\"reason\"]')", "role audit reason");
  await assertVisibleFocus(send, sessionId);
  await send("Input.insertText", { text: "Keyboard administration evidence" }, sessionId);
  await tabTo(send, sessionId, "document.activeElement?.textContent?.trim() === 'Change role'", "Change role button");
  await assertVisibleFocus(send, sessionId);
  await pressEnter(send, sessionId);
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('Updated Member')");

  await send("Emulation.setScriptExecutionDisabled", { value: false }, sessionId);
  const root = target.replace(/\/admin$/, "");

  await navigate(send, sessionId, `${root}/__test/empty`, "location.pathname.endsWith('/admin') && document.body.textContent.includes('Board administration')");
  assert.equal(await evaluate(send, sessionId, `(() => {
    const metrics = Object.fromEntries([...document.querySelectorAll("dl > div")].map((node) => [node.querySelector("dt").textContent.trim(), node.querySelector("dd").textContent.trim()]));
    return metrics.Accounts === "0" && metrics.Topics === "0" && metrics["Visible posts"] === "0";
  })()`), true);

  await navigate(send, sessionId, `${root}/__test/populated`, "location.pathname.endsWith('/admin') && document.body.textContent.includes('52')");
  await auditAccessibility(send, sessionId, evaluate, "populated administration dashboard", false);
  await auditReflow(send, sessionId, evaluate, "populated administration dashboard");

  await navigate(send, sessionId, `${target}/accounts`, "document.querySelectorAll('main li').length === 50 && document.body.textContent.includes('Next accounts')");
  assert.equal(await evaluate(send, sessionId, "[...document.querySelectorAll('main a')].some((link) => link.textContent.trim() === 'Local Member')"), true);
  assert.equal(await evaluate(send, sessionId, `(() => { const link = [...document.querySelectorAll("main a")].find((node) => node.textContent.trim() === "Next accounts"); link.click(); return true; })()`), true);
  await waitFor(send, sessionId, "location.search === '?after=51' && document.body.textContent.includes('Continuation Account')");

  await navigate(send, sessionId, `${target}/groups`, "document.body.textContent.includes('Groups') && typeof htmx !== 'undefined'");
  await submitForm(send, sessionId, `form[action$="/admin/groups"]`, { name: "Browser Operators", reason: "Create browser group" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/groups/4\"] input[name=\"name\"]')?.value === 'Browser Operators'");
  await submitForm(send, sessionId, `form[action$="/admin/groups/4"]`, { name: "Renamed Browser Operators", reason: "Rename browser group" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/groups/4\"] input[name=\"name\"]')?.value === 'Renamed Browser Operators'");
  assert.equal(await evaluate(send, sessionId, "document.querySelectorAll('main li').length === 50 && document.body.textContent.includes('Next groups')"), true);
  assert.equal(await evaluate(send, sessionId, `(() => { const link = [...document.querySelectorAll("main a")].find((node) => node.textContent.trim() === "Next groups"); link.click(); return true; })()`), true);
  await waitFor(send, sessionId, "location.search === '?after=53' && document.querySelector('form[action$=\"/admin/groups/54\"] input[name=\"name\"]')?.value === 'Continuation Group'");
  await evaluate(send, sessionId, "history.back()");
  await waitFor(send, sessionId, "location.search === '' && document.querySelector('form[action$=\"/admin/groups/4\"] input[name=\"name\"]')?.value === 'Renamed Browser Operators'");
  await evaluate(send, sessionId, "history.forward()");
  await waitFor(send, sessionId, "location.search === '?after=53' && document.querySelector('form[action$=\"/admin/groups/54\"] input[name=\"name\"]')?.value === 'Continuation Group'");

  await navigate(send, sessionId, `${target}/accounts/2`, "document.body.textContent.includes('Updated Member')");
  await auditAccessibility(send, sessionId, evaluate, "administration account detail", false);
  await submitForm(send, sessionId, `form[action$="/admin/accounts/2/groups/4"]`, { action: "revoke", reason: "Revoke browser membership" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/accounts/2/groups/4\"] button')?.textContent.trim() === 'Grant' && document.querySelector('form[action$=\"/admin/accounts/2/role\"] input[name=\"revision\"]')?.value === '5'");
  await navigate(send, sessionId, `${root}/__test/member`, "location.pathname.endsWith('/areas/restricted') && document.body.textContent.includes('Page not found')");
  await navigate(send, sessionId, `${root}/__test/admin`, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('Updated Member')");

  await navigate(send, sessionId, `${target}/accounts/2/sessions`, "document.body.textContent.includes('Local sessions for Updated Member') && document.querySelector('form[action*=\"/admin/sessions/\"]') !== null");
  await auditAccessibility(send, sessionId, evaluate, "administration member sessions", false);
  await auditReflow(send, sessionId, evaluate, "administration member sessions");
  await submitForm(send, sessionId, `form[action*="/admin/sessions/"]`, { reason: "Revoke browser member session" });
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/accounts/2/sessions') && document.body.textContent.includes('No active local sessions')");
  await navigate(send, sessionId, `${root}/__test/admin`, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('Updated Member')");
  await submitForm(send, sessionId, `form[action$="/admin/accounts/2/groups/4"]`, { action: "grant", reason: "Grant browser membership" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/accounts/2/groups/4\"] button')?.textContent.trim() === 'Revoke' && document.querySelector('form[action$=\"/admin/accounts/2/role\"] input[name=\"revision\"]')?.value === '6'");
  await navigate(send, sessionId, `${root}/__test/member`, "location.pathname.endsWith('/areas/restricted') && document.body.textContent.includes('Restricted browser area')");
  await navigate(send, sessionId, `${root}/__test/admin`, "location.pathname.endsWith('/admin/accounts/2') && document.body.textContent.includes('Updated Member')");

  await navigate(send, sessionId, `${target}/areas`, "document.body.textContent.includes('Create area') && document.querySelector('[data-area-identity-fields]') !== null");
  await auditAccessibility(send, sessionId, evaluate, "administration area create", false);
  await auditAreaFormPresentation(send, sessionId, "administration area create", `form[action$="/admin/areas"]`, ["[data-area-identity-fields]", "[data-area-policy-fields]"]);
  await auditReflow(send, sessionId, evaluate, "administration area create");

  await navigate(send, sessionId, `${target}/areas/3`, "document.body.textContent.includes('General') && document.body.textContent.includes('Group access')");
  await auditAccessibility(send, sessionId, evaluate, "administration area detail", false);
  await auditAreaFormPresentation(send, sessionId, "administration area detail", `form[action$="/admin/areas/3"]`, ["[data-area-policy-fields]"]);
  await auditAreaGroupActionPresentation(send, sessionId, "administration area detail");
  await auditReflow(send, sessionId, evaluate, "administration area detail");
  const areaSelector = `form[action$="/admin/areas/3"]`;
  const areaValues = { name: "General", description: "Browser area", display_order: "3", visibility: "groups", reason: "Change browser area" };
  await submitForm(send, sessionId, areaSelector, { ...areaValues, posting_mode: "archived" });
  await waitFor(send, sessionId, "document.querySelector('select[name=\"posting_mode\"]')?.value === 'archived' && document.querySelector('form[action$=\"/admin/areas/3\"] input[name=\"revision\"]')?.value === '3'");
  await submitForm(send, sessionId, areaSelector, { ...areaValues, posting_mode: "normal" });
  await waitFor(send, sessionId, "document.querySelector('select[name=\"posting_mode\"]')?.value === 'normal' && document.querySelector('form[action$=\"/admin/areas/3\"] input[name=\"revision\"]')?.value === '4'");

  const areaGroupSelector = `form[action$="/admin/areas/3/groups/4"]`;
  await submitForm(send, sessionId, areaGroupSelector, { reason: "Revoke browser area access" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/areas/3/groups/4\"] button')?.textContent.trim() === 'Grant' && document.querySelector('form[action$=\"/admin/areas/3\"] input[name=\"revision\"]')?.value === '5'");
  await submitForm(send, sessionId, areaGroupSelector, { reason: "Grant browser area access" });
  await waitFor(send, sessionId, "document.querySelector('form[action$=\"/admin/areas/3/groups/4\"] button')?.textContent.trim() === 'Revoke' && document.querySelector('form[action$=\"/admin/areas/3\"] input[name=\"revision\"]')?.value === '6'");

  await navigate(send, sessionId, `${target}/control`, "document.body.textContent.includes('Control settings') && document.querySelector('form[action$=\"/admin/control\"]') !== null");
  await auditAccessibility(send, sessionId, evaluate, "administration control settings", false);
  await auditReflow(send, sessionId, evaluate, "administration control settings");
  await submitForm(send, sessionId, `form[action$="/admin/control"]`, {
    registration_mode: "invitation_only",
    maintenance_enabled: "disabled",
    maintenance_message: "Browser maintenance",
    publish_rate_limit: "8",
    new_account_publish_rate_limit: "3",
    publish_window_seconds: "60",
    new_account_period_seconds: "86400",
    session_idle_seconds: "1800",
    auth_revalidate_seconds: "900",
    reason: "Update browser control",
  });
  await waitFor(send, sessionId, "document.querySelector('select[name=\"registration_mode\"]')?.value === 'invitation_only' && document.querySelector('input[name=\"revision\"]')?.value === '5'");

  await navigate(send, sessionId, `${target}/email`, "document.body.textContent.includes('Shared SMTP transport is configured') && document.querySelector('form[action$=\"/admin/email/test\"]') !== null");
  await auditAccessibility(send, sessionId, evaluate, "administration email", false);
  await auditReflow(send, sessionId, evaluate, "administration email");
  await submitForm(send, sessionId, `form[action$="/admin/email/test"]`, { reason: "Verify browser email" });
  await waitFor(send, sessionId, "location.pathname.endsWith('/admin/email') && document.body.textContent.includes('Last result') && document.body.textContent.includes('accepted')");

  await navigate(send, sessionId, `${target}/settings`, "document.body.textContent.includes('Site settings')");
  await submitForm(send, sessionId, `form[action$="/admin/settings"]`, {
    site_name: "Updated Browser Board",
    site_description: "Updated browser description",
    brand_theme: "rose",
    rules_markdown: "# Updated browser rules",
    reason: "Update browser presentation",
  });
  await waitFor(send, sessionId, "document.body.textContent.includes('Updated Browser Board') && document.documentElement.dataset.brandTheme === 'rose'");
  await navigate(send, sessionId, `${root}/rules`, "document.body.textContent.includes('Updated browser rules') && document.body.textContent.includes('Updated Browser Board')");

  await navigate(send, sessionId, `${target}/accounts/1/sessions`, "document.body.textContent.includes('Local sessions for Browser Administrator')");
  await auditAccessibility(send, sessionId, evaluate, "administration current sessions", false);
  await auditReflow(send, sessionId, evaluate, "administration current sessions");
  await submitForm(send, sessionId, `form[action$="/admin/accounts/1/sessions/revoke"]`, { reason: "Revoke browser administrator sessions" });
  await waitFor(send, sessionId, "location.pathname.endsWith('/login')");
});

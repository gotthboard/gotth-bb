import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const axeSource = await readFile(new URL("../../node_modules/axe-core/axe.min.js", import.meta.url), "utf8");

export async function auditAccessibility(send, sessionId, evaluate, label, scriptDisabled = true) {
  if (scriptDisabled) await send("Emulation.setScriptExecutionDisabled", { value: false }, sessionId);
  try {
    await evaluate(send, sessionId, axeSource);
    const result = await evaluate(send, sessionId, `axe.run(document, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] },
    resultTypes: ["violations", "incomplete"]
  }).then(({ violations, incomplete }) => ({
    violations: violations.map(({ id, impact, help, nodes }) => ({
      id, impact, help,
      nodes: nodes.map(({ target, failureSummary }) => ({ target, failureSummary }))
    })),
    incomplete: incomplete.map(({ id, impact, nodes }) => ({
      id, impact,
      nodes: nodes.map(({ target, failureSummary, any, all, none }) => ({ target, failureSummary, any, all, none }))
    }))
    }))`);
    assert.deepEqual(result.violations, [], `${label} accessibility violations: ${JSON.stringify(result.violations)}`);
    console.log(`A11Y label=${JSON.stringify(label)} violations=0 incomplete=${JSON.stringify(result.incomplete)}`);
    return result;
  } finally {
    if (scriptDisabled) await send("Emulation.setScriptExecutionDisabled", { value: true }, sessionId);
  }
}

export async function auditReflow(send, sessionId, evaluate, label) {
  await send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  const narrow = await evaluate(send, sessionId, "({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth })");
  assert.ok(narrow.scrollWidth <= narrow.clientWidth, `${label} overflows at 320 CSS pixels: scrollWidth=${narrow.scrollWidth} clientWidth=${narrow.clientWidth}`);
  console.log(`REFLOW label=${JSON.stringify(label)} width=320 zoom=100 scrollWidth=${narrow.scrollWidth} clientWidth=${narrow.clientWidth} result=pass`);

  await send("Emulation.setDeviceMetricsOverride", { width: 640, height: 720, deviceScaleFactor: 1, mobile: false }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 2 }, sessionId);
  const zoomed = await evaluate(send, sessionId, "({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth })");
  assert.ok(zoomed.scrollWidth <= zoomed.clientWidth, `${label} overflows at 200% zoom: scrollWidth=${zoomed.scrollWidth} clientWidth=${zoomed.clientWidth}`);
  console.log(`REFLOW label=${JSON.stringify(label)} width=640 zoom=200 scrollWidth=${zoomed.scrollWidth} clientWidth=${zoomed.clientWidth} result=pass`);

  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId);
}

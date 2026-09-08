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
    incomplete: incomplete.map(({ id, impact, nodes }) => ({ id, impact, nodes: nodes.length }))
  }))`);
    assert.deepEqual(result.violations, [], `${label} accessibility violations: ${JSON.stringify(result.violations)}`);
    return result;
  } finally {
    if (scriptDisabled) await send("Emulation.setScriptExecutionDisabled", { value: true }, sessionId);
  }
}

export async function auditReflow(send, sessionId, evaluate, label) {
  await send("Emulation.setDeviceMetricsOverride", { width: 320, height: 640, deviceScaleFactor: 1, mobile: true }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  assert.equal(await evaluate(send, sessionId, "document.documentElement.scrollWidth <= innerWidth"), true, `${label} overflows at 320 CSS pixels`);

  await send("Emulation.setDeviceMetricsOverride", { width: 640, height: 720, deviceScaleFactor: 1, mobile: false }, sessionId);
  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 2 }, sessionId);
  assert.equal(await evaluate(send, sessionId, "document.documentElement.scrollWidth <= innerWidth"), true, `${label} overflows at 200% zoom`);

  await send("Emulation.setPageScaleFactor", { pageScaleFactor: 1 }, sessionId);
  await send("Emulation.setDeviceMetricsOverride", { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId);
}

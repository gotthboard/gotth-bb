import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const script = readFileSync(new URL("./markdown-toolbar.js", import.meta.url), "utf8");

function load(document = { querySelectorAll: () => [], addEventListener: () => {} }) {
  const context = { document };
  context.globalThis = context;
  vm.runInNewContext(script, context, { filename: "markdown-toolbar.js" });
  return context.gotthMarkdownToolbar;
}

test("inline actions preserve and toggle selections", () => {
  const { transform } = load();
  let result = transform("word", 0, 4, "bold");
  assert.deepEqual({ ...result }, { value: "**word**", start: 2, end: 6 });
  result = transform(result.value, result.start, result.end, "bold");
  assert.deepEqual({ ...result }, { value: "word", start: 0, end: 4 });

  result = transform("", 0, 0, "italic");
  assert.deepEqual({ ...result }, { value: "*text*", start: 1, end: 5 });
  assert.equal(transform("gone", 0, 4, "strike").value, "~~gone~~");
  assert.equal(transform("code", 0, 4, "inline-code").value, "`code`");
});

test("line actions handle multiline selections and toggle", () => {
  const { transform } = load();
  for (const [action, want] of [
    ["quote", "> one\n> two"],
    ["unordered-list", "- one\n- two"],
    ["ordered-list", "1. one\n2. two"],
    ["task-list", "- [ ] one\n- [ ] two"],
  ]) {
    const added = transform("one\ntwo", 0, 7, action);
    assert.equal(added.value, want);
    assert.equal(transform(added.value, added.start, added.end, action).value, "one\ntwo");
  }

  assert.equal(transform("one\ntwo", 0, 4, "quote").value, "> one\ntwo");
  assert.equal(transform("one\ntwo", 4, 4, "quote").value, "one\n> two");
});

test("block, link, and table insertions keep useful edit selections", () => {
  const { transform } = load();
  let result = transform("line one\nline two", 0, 17, "fenced-code");
  assert.equal(result.value, "```\nline one\nline two\n```");
  assert.equal(transform(result.value, result.start, result.end, "fenced-code").value, "line one\nline two");

  result = transform("label", 0, 5, "link");
  assert.equal(result.value, "[label](https://example.org)");
  assert.equal(result.value.slice(result.start, result.end), "https://example.org");

  result = transform("cell", 0, 4, "table");
  assert.match(result.value, /^\| cell \| Column 2 \|/);
  assert.equal(result.value.slice(result.start, result.end), "cell");
});

test("unsupported Image action is a no-op", () => {
  const { transform } = load();
  assert.deepEqual({ ...transform("draft", 1, 4, "image") }, { value: "draft", start: 1, end: 4 });
});

test("enhancement wires buttons without replacing ordinary textarea behavior", () => {
  let click;
  const textarea = {
    value: "word",
    selectionStart: 0,
    selectionEnd: 4,
    focusCalls: 0,
    focus() { this.focusCalls += 1; },
    setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; },
  };
  const button = {
    dataset: { markdownAction: "bold" },
    addEventListener(name, listener) { if (name === "click") click = listener; },
  };
  const toolbar = { hidden: true };
  const root = {
    dataset: {},
    querySelector(selector) {
      if (selector === "textarea") return textarea;
      if (selector === "[data-markdown-toolbar]") return toolbar;
      return null;
    },
    querySelectorAll: () => [button],
  };
  const document = {
    querySelectorAll: () => [root],
    addEventListener: () => {},
  };
  load(document);
  assert.equal(toolbar.hidden, false);
  click({ preventDefault() {} });
  assert.equal(textarea.value, "**word**");
  assert.equal(textarea.focusCalls, 1);
  assert.equal(root.dataset.markdownEnhanced, "true");
});

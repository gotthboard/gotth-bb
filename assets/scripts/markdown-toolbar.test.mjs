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
  result = transform("code", 0, 4, "inline-code");
  assert.deepEqual({ ...result }, { value: "` code `", start: 2, end: 6 });
  assert.deepEqual({ ...transform(result.value, result.start, result.end, "inline-code") }, { value: "code", start: 0, end: 4 });
});

test("line actions preserve exact caret and selection coordinates", () => {
  const { transform } = load();
  for (const [action, prefixOne, prefixTwo] of [
    ["quote", "> ", "> "],
    ["unordered-list", "- ", "- "],
    ["ordered-list", "1. ", "2. "],
    ["task-list", "- [ ] ", "- [ ] "],
  ]) {
    let added = transform("one\ntwo", 1, 6, action);
    assert.deepEqual({ ...added }, {
      value: prefixOne + "one\n" + prefixTwo + "two",
      start: prefixOne.length + 1,
      end: prefixOne.length + 4 + prefixTwo.length + 2,
    });
    assert.deepEqual({ ...transform(added.value, added.start, added.end, action) }, { value: "one\ntwo", start: 1, end: 6 });

    added = transform("one", 1, 1, action);
    assert.deepEqual({ ...added }, { value: prefixOne + "one", start: prefixOne.length + 1, end: prefixOne.length + 1 });

    added = transform("", 0, 0, action);
    assert.deepEqual({ ...added }, { value: prefixOne, start: prefixOne.length, end: prefixOne.length });

    added = transform("one\ntwo", 0, 4, action);
    assert.deepEqual({ ...added }, { value: prefixOne + "one\ntwo", start: prefixOne.length, end: prefixOne.length + 4 });

    added = transform("before one after", 8, 11, action);
    assert.deepEqual({ ...added }, {
      value: prefixOne + "before one after",
      start: prefixOne.length + 8,
      end: prefixOne.length + 11,
    });

    const prefixed = prefixOne + "one\n" + prefixTwo + "two";
    added = transform(prefixed, prefixOne.length + 1, prefixOne.length + 4 + prefixTwo.length + 2, action);
    assert.deepEqual({ ...added }, { value: "one\ntwo", start: 1, end: 6 });

    added = transform("zero\n" + prefixOne + "one", 5 + prefixOne.length + 1, 5 + prefixOne.length + 1, action);
    assert.deepEqual({ ...added }, { value: "zero\none", start: 6, end: 6 });

    added = transform("\nabc", 0, 0, action);
    assert.deepEqual({ ...added }, { value: prefixOne + "\nabc", start: prefixOne.length, end: prefixOne.length });

    added = transform("\r\nabc", 0, 0, action);
    assert.deepEqual({ ...added }, { value: prefixOne + "\r\nabc", start: prefixOne.length, end: prefixOne.length });
  }
});

test("block, link, and table insertions keep useful edit selections", () => {
  const { transform } = load();
  let result = transform("line one\nline two", 0, 17, "fenced-code");
  assert.equal(result.value, "```\nline one\nline two\n```");
  assert.equal(transform(result.value, result.start, result.end, "fenced-code").value, "line one\nline two");

  result = transform("a`b", 0, 3, "inline-code");
  assert.deepEqual({ ...result }, { value: "`` a`b ``", start: 3, end: 6 });
  assert.deepEqual({ ...transform(result.value, result.start, result.end, "inline-code") }, { value: "a`b", start: 0, end: 3 });

  result = transform("before\n```\nafter", 0, 16, "fenced-code");
  assert.equal(result.value, "````\nbefore\n```\nafter\n````");
  assert.equal(result.value.slice(result.start, result.end), "before\n```\nafter");
  assert.deepEqual({ ...transform(result.value, result.start, result.end, "fenced-code") }, { value: "before\n```\nafter", start: 0, end: 16 });

  result = transform("label", 0, 5, "link");
  assert.equal(result.value, "[label](https://example.org)");
  assert.equal(result.value.slice(result.start, result.end), "https://example.org");

  result = transform("cell", 0, 4, "table");
  assert.match(result.value, /^\| cell \| Column 2 \|/);
  assert.equal(result.value.slice(result.start, result.end), "cell");
  assert.deepEqual({ ...transform(result.value, result.start, result.end, "table") }, { value: "cell", start: 0, end: 4 });
});

test("mid-line blocks add only required boundaries and round-trip exactly", () => {
  const { transform } = load();
  for (const [source, start, end, eol] of [
    ["before code after", 7, 11, "\n"],
    ["before\r\ncode\r\nafter", 8, 12, "\r\n"],
    ["before\ncode\nafter", 7, 11, "\n"],
  ]) {
    let result = transform(source, start, end, "fenced-code");
    assert.equal(result.value.slice(result.start, result.end), "code");
    assert.match(result.value, new RegExp("`{3,}" + eol + "code" + eol + "`{3,}"));
    assert.deepEqual({ ...transform(result.value, result.start, result.end, "fenced-code") }, { value: source, start, end });

    result = transform(source, start, end, "table");
    assert.equal(result.value.slice(result.start, result.end), "code");
    assert.match(result.value, new RegExp("\\| code \\| Column 2 \\|" + eol + "\\| -{3,4} \\| -{3,4} \\|"));
    assert.deepEqual({ ...transform(result.value, result.start, result.end, "table") }, { value: source, start, end });
  }

  let result = transform("before code after", 7, 11, "fenced-code");
  assert.equal(result.value, "before \n``````\ncode\n``````\n after");
  result = transform("before cell after", 7, 11, "table");
  assert.equal(result.value, "before \n| cell | Column 2 |\n| ---- | ---- |\n| Cell 1 | Cell 2 |\n after");
});

test("all-space inline code preserves and toggles exact selected spaces", () => {
  const { transform } = load();
  for (const source of [" ", "   "]) {
    const result = transform(source, 0, source.length, "inline-code");
    assert.equal(result.value, "`" + source + "`");
    assert.equal(result.value.slice(result.start, result.end), source);
    assert.deepEqual({ ...transform(result.value, result.start, result.end, "inline-code") }, { value: source, start: 0, end: source.length });
  }
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

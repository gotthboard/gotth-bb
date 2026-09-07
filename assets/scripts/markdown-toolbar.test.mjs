import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const script = readFileSync(new URL("./markdown-toolbar.js", import.meta.url), "utf8");

function load(document = { querySelectorAll: () => [], addEventListener: () => {} }) {
  const context = { document, setTimeout };
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

test("star actions compose without consuming authored delimiter runs", () => {
  const { transform } = load();
  const cases = [
    { name: "italic inside strong", action: "italic", source: "**word**", start: 2, end: 6, value: "***word***", next: { value: "**word**", start: 2, end: 6 } },
    { name: "strong inside emphasis", action: "bold", source: "*word*", start: 1, end: 5, value: "***word***", next: { value: "*word*", start: 1, end: 5 } },
    { name: "italic off combined", action: "italic", source: "***word***", start: 3, end: 7, value: "**word**", next: { value: "***word***", start: 3, end: 7 } },
    { name: "strong off combined", action: "bold", source: "***word***", start: 3, end: 7, value: "*word*", next: { value: "***word***", start: 3, end: 7 } },
    { name: "left adjacent", action: "italic", source: "*word", start: 1, end: 5, value: "\\**word*", next: { value: "*word", start: 1, end: 5 } },
    { name: "right multiple adjacent", action: "italic", source: "word**", start: 0, end: 4, value: "*word*\\*\\*", next: { value: "word**", start: 0, end: 4 } },
    { name: "unequal both adjacent", action: "italic", source: "**word*", start: 2, end: 6, value: "\\*\\**word*\\*", next: { value: "**word*", start: 2, end: 6 } },
    { name: "bold unequal both adjacent", action: "bold", source: "*word**", start: 1, end: 5, value: "\\***word**\\*\\*", next: { value: "*word**", start: 1, end: 5 } },
    { name: "padded strong", action: "italic", source: "** word **", start: 2, end: 8, value: "*** word ***", next: { value: "** word **", start: 2, end: 8 } },
  ];
  for (const item of cases) {
    const changed = transform(item.source, item.start, item.end, item.action);
    assert.equal(changed.value, item.value, item.name);
    assert.equal(changed.value.slice(changed.start, changed.end), item.source.slice(item.start, item.end), item.name);
    assert.deepEqual({ ...transform(changed.value, changed.start, changed.end, item.action) }, item.next, item.name);
  }

  let whole = transform("**word**", 0, 8, "italic");
  assert.deepEqual({ ...whole }, { value: "***word***", start: 1, end: 9 });
  assert.deepEqual({ ...transform(whole.value, whole.start, whole.end, "italic") }, { value: "**word**", start: 0, end: 8 });
  whole = transform("*word*", 0, 6, "bold");
  assert.deepEqual({ ...whole }, { value: "***word***", start: 2, end: 8 });
  assert.deepEqual({ ...transform(whole.value, whole.start, whole.end, "bold") }, { value: "*word*", start: 0, end: 6 });

  const caret = transform("*", 1, 1, "italic");
  assert.deepEqual({ ...caret }, { value: "\\**text*", start: 3, end: 7 });
  assert.deepEqual({ ...transform(caret.value, caret.start, caret.end, "italic") }, { value: "*text", start: 1, end: 5 });

  const combinedCaret = transform("****", 2, 2, "italic");
  assert.deepEqual({ ...combinedCaret }, { value: "***text***", start: 3, end: 7 });
  assert.deepEqual({ ...transform(combinedCaret.value, combinedCaret.start, combinedCaret.end, "italic") }, { value: "**text**", start: 2, end: 6 });
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

test("block actions expand to complete lines and preserve mapped selections", () => {
  const { transform } = load();
  for (const [source, start, end, eol] of [
    ["before code after", 7, 11, "\n"],
    ["before\r\ncode\r\nafter", 8, 12, "\r\n"],
    ["before\ncode\nafter", 7, 11, "\n"],
  ]) {
    let result = transform(source, start, end, "fenced-code");
    assert.equal(result.value.slice(result.start, result.end), "code");
    const completeLine = source.includes("before code") ? "before code after" : "code";
    assert.match(result.value, new RegExp("`{3,}" + eol + completeLine + eol + "`{3,}"));
    assert.deepEqual({ ...transform(result.value, result.start, result.end, "fenced-code") }, { value: source, start, end });

    result = transform(source, start, end, "table");
    assert.equal(result.value.slice(result.start, result.end), "code");
    assert.match(result.value, new RegExp("\\| " + completeLine + " \\| Column 2 \\|" + eol + "\\| --- \\| --- \\|"));
    assert.deepEqual({ ...transform(result.value, result.start, result.end, "table") }, { value: source, start, end });
  }

  let result = transform("before code after", 7, 11, "fenced-code");
  assert.equal(result.value, "```\nbefore code after\n```");
  result = transform("before cell after", 7, 11, "table");
  assert.equal(result.value, "| before cell after | Column 2 |\n| --- | --- |");
});

test("block actions preserve multiline LF and uniform CRLF source exactly", () => {
  const { transform } = load();
  for (const [source, selected] of [
    ["before\none\ntwo\nafter", "one\ntwo"],
    ["before\r\none\r\ntwo\r\nafter", "one\r\ntwo"],
  ]) {
    const start = source.indexOf(selected);
    const end = start + selected.length;
    for (const action of ["fenced-code", "table"]) {
      const result = transform(source, start, end, action);
      assert.equal(result.value.slice(result.start, result.end).includes("one"), true);
      assert.deepEqual({ ...transform(result.value, result.start, result.end, action) }, { value: source, start, end });
    }
  }
});

test("authored fences and table dash variants never consume exterior newlines", () => {
  const { transform } = load();
  for (const length of [3, 4, 5, 6, 9]) {
    const marker = "`".repeat(length);
    const source = "before\n" + marker + "\ncode\n" + marker + "\nafter";
    const start = source.indexOf("code");
    const result = transform(source, start, start + 4, "fenced-code");
    assert.deepEqual({ ...result }, { value: "before\ncode\nafter", start: 7, end: 11 });
  }
  for (const [openingLength, closingLength] of [[3, 4], [4, 7]]) {
    const opening = "`".repeat(openingLength);
    const closing = "`".repeat(closingLength);
    const source = "before\n" + opening + "\ncode\n" + closing + "\nafter";
    const start = source.indexOf("code");
    const result = transform(source, start, start + 4, "fenced-code");
    assert.deepEqual({ ...result }, { value: "before\ncode\nafter", start: 7, end: 11 });
  }
  for (const [left, right] of [[3, 3], [4, 3], [3, 4], [4, 4], [7, 6]]) {
    const source = "before\n| cell | Column 2 |\n| " + "-".repeat(left) + " | " + "-".repeat(right) + " |\n| Cell 1 | Cell 2 |\nafter";
    const start = source.indexOf("cell");
    const result = transform(source, start, start + 4, "table");
    assert.deepEqual({ ...result }, { value: "before\ncell\nCell 1\nafter", start: 7, end: 11 });
  }
});

test("table action round-trips escaped first-column text and mapped selection", () => {
  const { transform } = load();
  const source = "before\na|b\\c\nd|e\\f\nafter";
  const selected = "b\\c\nd|e";
  const start = source.indexOf(selected);
  const end = start + selected.length;
  const table = transform(source, start, end, "table");
  assert.match(table.value, /\| a\\\|b\\\\c \| Column 2 \|/);
  assert.match(table.value, /\| d\\\|e\\\\f \| Cell 2 \|/);
  assert.equal(table.value.slice(table.start, table.end), "b\\\\c | Column 2 |\n| --- | --- |\n| d\\|e");
  assert.deepEqual({ ...transform(table.value, table.start, table.end, "table") }, { value: source, start, end });
});

test("block actions use adaptive fences and bounded empty placeholders", () => {
  const { transform } = load();
  const source = "prefix `````` suffix";
  const start = source.indexOf("``````");
  const fenced = transform(source, start, start + 6, "fenced-code");
  assert.match(fenced.value, /^`{7}\n/);
  assert.deepEqual({ ...transform(fenced.value, fenced.start, fenced.end, "fenced-code") }, { value: source, start, end: start + 6 });

  let result = transform("", 0, 0, "fenced-code");
  assert.deepEqual({ value: result.value, selected: result.value.slice(result.start, result.end) }, { value: "```\ncode\n```", selected: "code" });
  result = transform("", 0, 0, "table");
  assert.deepEqual({ value: result.value, selected: result.value.slice(result.start, result.end) }, { value: "| Column 1 | Column 2 |\n| --- | --- |", selected: "Column 1" });
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

test("inline code preserves adjacent authored backtick runs exactly", () => {
  const { transform } = load();
  const cases = [
    { name: "right single", source: "text`", start: 0, end: 4, wrapped: "` text `\\`" },
    { name: "left single", source: "`text", start: 1, end: 5, wrapped: "\\`` text `" },
    { name: "both single", source: "x`text`y", start: 2, end: 6, wrapped: "x\\`` text `\\`y" },
    { name: "unequal multiple", source: "x```text``y", start: 4, end: 8, wrapped: "x\\`\\`\\`` text `\\`\\`y" },
    { name: "internal and exterior runs", source: "x``a`b```y", start: 3, end: 6, wrapped: "x\\`\\``` a`b ``\\`\\`\\`y" },
    { name: "padded content", source: "x` text `y", start: 2, end: 8, wrapped: "x\\``  text  `\\`y" },
    { name: "all-space content", source: "x`   ``y", start: 2, end: 5, wrapped: "x\\``   `\\`\\`y" },
  ];
  for (const item of cases) {
    const wrapped = transform(item.source, item.start, item.end, "inline-code");
    assert.equal(wrapped.value, item.wrapped, item.name);
    assert.equal(wrapped.value.slice(wrapped.start, wrapped.end), item.source.slice(item.start, item.end), item.name);
    assert.deepEqual(
      { ...transform(wrapped.value, wrapped.start, wrapped.end, "inline-code") },
      { value: item.source, start: item.start, end: item.end },
      item.name,
    );
  }

  const caret = transform("x`y", 2, 2, "inline-code");
  assert.deepEqual({ ...caret }, { value: "x\\`` code `y", start: 5, end: 9 });
  assert.deepEqual(
    { ...transform(caret.value, caret.start, caret.end, "inline-code") },
    { value: "x`codey", start: 2, end: 6 },
  );

  const noncanonical = transform("x`` text ``y", 4, 8, "inline-code");
  assert.equal(noncanonical.value, "x`` ` text ` ``y");
  assert.equal(noncanonical.value.slice(noncanonical.start, noncanonical.end), "text");
});

test("unsupported Image action is a no-op", () => {
  const { transform } = load();
  assert.deepEqual({ ...transform("draft", 1, 4, "image") }, { value: "draft", start: 1, end: 4 });
});

test("enhancement wires buttons without replacing ordinary textarea behavior", () => {
  const editor = editorHarness();
  const document = {
    querySelectorAll: () => [editor.root],
    addEventListener: () => {},
  };
  load(document);
  assert.equal(editor.toolbar.hidden, false);
  editor.click();
  assert.equal(editor.textarea.value, "**word**");
  assert.equal(editor.textarea.focusCalls, 1);
  assert.equal(editor.root.dataset.markdownEnhanced, "true");
  assert.equal(typeof editor.textareaListeners.compositionstart, "function");
  assert.equal(typeof editor.textareaListeners.compositionend, "function");
});

function editorHarness(value = "word", action = "bold") {
  const textareaListeners = {};
  const buttonListeners = {};
  const rootListeners = {};
  const textarea = {
    value,
    selectionStart: 0,
    selectionEnd: value.length,
    focusCalls: 0,
    focus() { this.focusCalls += 1; },
    setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; },
    addEventListener(name, listener) { textareaListeners[name] = listener; },
  };
  const button = {
    dataset: { markdownAction: action },
    addEventListener(name, listener) { buttonListeners[name] = listener; },
  };
  const toolbar = { hidden: true };
  const root = {
    dataset: {},
    addEventListener(name, listener) { rootListeners[name] = listener; },
    removeEventListener(name, listener) { if (rootListeners[name] === listener) delete rootListeners[name]; },
    querySelector(selector) {
      if (selector === "textarea") return textarea;
      if (selector === "[data-markdown-toolbar]") return toolbar;
      return null;
    },
    querySelectorAll: () => [button],
  };
  const click = (detail = 0, pointerId) => {
    let prevented = 0;
    buttonListeners.click({ detail, pointerId, button: 0, preventDefault() { prevented += 1; } });
    return prevented;
  };
  const dispatch = (name, properties = {}) => {
    let prevented = 0;
    buttonListeners[name]({ pointerId: 1, button: 0, ...properties, preventDefault() { prevented += 1; } });
    return prevented;
  };
  return { root, textarea, toolbar, button, rootListeners, textareaListeners, buttonListeners, click, dispatch };
}

function documentHarness(editors) {
  const listeners = new Map();
  let afterSwap;
  return {
    document: {
      querySelectorAll: () => editors.map((editor) => editor.root),
      addEventListener(name, listener) {
        if (name === "htmx:afterSwap") afterSwap = listener;
        if (!listeners.has(name)) listeners.set(name, new Set());
        listeners.get(name).add(listener);
      },
      removeEventListener(name, listener) { listeners.get(name)?.delete(listener); },
    },
    dispatch(name, properties = {}) {
      for (const listener of [...(listeners.get(name) || [])]) listener({ pointerId: 1, button: 0, ...properties });
    },
    afterSwap(event) { afterSwap(event); },
  };
}

test("live IME composition makes toolbar actions strict editor-local no-ops", () => {
  const first = editorHarness("provisional", "bold");
  const second = editorHarness("stable", "italic");
  let afterSwap;
  const document = {
    querySelectorAll: () => [first.root, second.root],
    addEventListener(name, listener) { if (name === "htmx:afterSwap") afterSwap = listener; },
  };
  load(document);

  first.textarea.selectionStart = 2;
  first.textarea.selectionEnd = 7;
  first.textareaListeners.compositionstart();
  assert.equal(first.click(), 1);
  assert.equal(first.textarea.value, "provisional");
  assert.equal(first.textarea.selectionStart, 2);
  assert.equal(first.textarea.selectionEnd, 7);
  assert.equal(first.textarea.focusCalls, 0);

  assert.equal(second.click(), 1);
  assert.equal(second.textarea.value, "*stable*");
  assert.equal(second.textarea.focusCalls, 1);

  first.textareaListeners.compositionend();
  assert.equal(first.click(), 1);
  assert.equal(first.textarea.value, "pr**ovisi**onal");
  assert.equal(first.textarea.selectionStart, 4);
  assert.equal(first.textarea.selectionEnd, 9);
  assert.equal(first.textarea.focusCalls, 1);

  const replacement = editorHarness("fresh", "strike");
  const swappedContainer = { querySelectorAll: () => [replacement.root] };
  first.textareaListeners.compositionstart();
  afterSwap({ target: swappedContainer });
  assert.equal(replacement.toolbar.hidden, false);
  replacement.click();
  assert.equal(replacement.textarea.value, "~~fresh~~");
  replacement.click();
  assert.equal(replacement.textarea.value, "fresh");
  replacement.textarea.focusCalls = 0;
  replacement.textarea.selectionStart = 1;
  replacement.textarea.selectionEnd = 4;
  replacement.textareaListeners.compositionstart();
  replacement.click();
  assert.equal(replacement.textarea.value, "fresh");
  assert.equal(replacement.textarea.selectionStart, 1);
  assert.equal(replacement.textarea.selectionEnd, 4);
  assert.equal(replacement.textarea.focusCalls, 0);
  replacement.textareaListeners.compositionend();
  replacement.click();
  assert.equal(replacement.textarea.value, "f~~res~~h");
  assert.equal(replacement.textarea.focusCalls, 1);
});

test("pointer activation that begins during composition stays inert after compositionend", () => {
  const first = editorHarness("provisional", "bold");
  const second = editorHarness("stable", "italic");
  const harness = documentHarness([first, second]);
  load(harness.document);

  first.textarea.selectionStart = 2;
  first.textarea.selectionEnd = 7;
  first.textareaListeners.compositionstart();
  assert.equal(first.dispatch("pointerdown"), 1);
  // A browser may end composition between pointerdown and its compatibility
  // mousedown. That mousedown must not erase the blocked-gesture latch.
  first.textareaListeners.compositionend();
  assert.equal(first.dispatch("mousedown"), 0);
  assert.equal(first.click(1, 1), 1);
  assert.equal(first.textarea.value, "provisional");
  assert.equal(first.textarea.selectionStart, 2);
  assert.equal(first.textarea.selectionEnd, 7);
  assert.equal(first.textarea.focusCalls, 0);

  assert.equal(second.dispatch("pointerdown"), 0);
  assert.equal(second.dispatch("mousedown"), 0);
  assert.equal(second.click(1, 1), 1);
  assert.equal(second.textarea.value, "*stable*");
  assert.equal(second.textarea.focusCalls, 1);

  // A later pointer gesture starts with a fresh non-composing pointerdown and
  // therefore follows the ordinary action path.
  assert.equal(first.dispatch("pointerdown"), 0);
  assert.equal(first.dispatch("mousedown"), 0);
  assert.equal(first.click(1, 1), 1);
  assert.equal(first.textarea.value, "pr**ovisi**onal");
  assert.equal(first.textarea.focusCalls, 1);

  const replacement = editorHarness("fresh", "strike");
  harness.afterSwap({ target: { querySelectorAll: () => [replacement.root] } });
  replacement.textarea.selectionStart = 1;
  replacement.textarea.selectionEnd = 4;
  replacement.textareaListeners.compositionstart();
  assert.equal(replacement.dispatch("mousedown"), 1);
  replacement.textareaListeners.compositionend();
  assert.equal(replacement.click(1, 1), 1);
  assert.equal(replacement.textarea.value, "fresh");
  assert.equal(replacement.textarea.selectionStart, 1);
  assert.equal(replacement.textarea.selectionEnd, 4);
  assert.equal(replacement.textarea.focusCalls, 0);
});

test("canceled and off-button pointer gestures cannot poison later native activation", async () => {
  const editor = editorHarness("word", "bold");
  const harness = documentHarness([editor]);
  load(harness.document);

  editor.textareaListeners.compositionstart();
  assert.equal(editor.dispatch("pointerdown", { pointerId: 7 }), 1);
  harness.dispatch("pointercancel", { pointerId: 7 });
  editor.textareaListeners.compositionend();
  assert.equal(editor.click(0), 1);
  assert.equal(editor.textarea.value, "**word**");
  assert.equal(editor.textarea.focusCalls, 1);

  editor.textarea.value = "word";
  editor.textarea.selectionStart = 0;
  editor.textarea.selectionEnd = 4;
  editor.textarea.focusCalls = 0;
  editor.textareaListeners.compositionstart();
  assert.equal(editor.dispatch("pointerdown", { pointerId: 8 }), 1);
  harness.dispatch("pointerup", { pointerId: 8 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  editor.textareaListeners.compositionend();
  assert.equal(editor.click(0), 1);
  assert.equal(editor.textarea.value, "**word**");
  assert.equal(editor.textarea.focusCalls, 1);

  const replacement = editorHarness("replacement", "italic");
  editor.textareaListeners.compositionstart();
  editor.dispatch("pointerdown", { pointerId: 9 });
  editor.rootListeners["htmx:beforeCleanupElement"]({ target: editor.root });
  assert.equal(editor.rootListeners["htmx:beforeCleanupElement"], undefined);
  harness.afterSwap({ target: { querySelectorAll: () => [replacement.root] } });
  replacement.click(0);
  assert.equal(replacement.textarea.value, "*replacement*");
});

test("mouse fallback, lost capture, multiple pointers, and buttons retire locally", async () => {
  const first = editorHarness("one", "bold");
  const second = editorHarness("two", "italic");
  const harness = documentHarness([first, second]);
  load(harness.document);

  first.textareaListeners.compositionstart();
  assert.equal(first.dispatch("mousedown", { button: 0 }), 1);
  harness.dispatch("mouseup", { button: 0 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  first.textareaListeners.compositionend();
  first.click(0);
  assert.equal(first.textarea.value, "**one**");

  first.textarea.value = "one";
  first.textarea.selectionStart = 0;
  first.textarea.selectionEnd = 3;
  first.textareaListeners.compositionstart();
  first.dispatch("pointerdown", { pointerId: 11 });
  first.dispatch("pointerdown", { pointerId: 12 });
  first.dispatch("lostpointercapture", { pointerId: 11 });
  harness.dispatch("pointercancel", { pointerId: 12 });
  first.textareaListeners.compositionend();
  first.click(0);
  assert.equal(first.textarea.value, "**one**");

  // The second editor/button never shares the first editor's gesture state.
  second.click(0);
  assert.equal(second.textarea.value, "*two*");

  const shared = editorHarness("shared", "bold");
  const italicListeners = {};
  const italicButton = {
    dataset: { markdownAction: "italic" },
    addEventListener(name, listener) { italicListeners[name] = listener; },
  };
  shared.root.querySelectorAll = () => [shared.button, italicButton];
  const sharedHarness = documentHarness([shared]);
  load(sharedHarness.document);
  shared.textareaListeners.compositionstart();
  shared.dispatch("pointerdown", { pointerId: 21 });
  shared.textareaListeners.compositionend();
  italicListeners.click({ detail: 0, button: 0, preventDefault() {} });
  assert.equal(shared.textarea.value, "*shared*");
  shared.click(1, 21);
  assert.equal(shared.textarea.value, "*shared*");
});

test("keyboard activation retains native click behavior outside composition", () => {
  const editor = editorHarness("word", "bold");
  load({ querySelectorAll: () => [editor.root], addEventListener: () => {} });

  editor.textareaListeners.compositionstart();
  assert.equal(editor.click(), 1);
  assert.equal(editor.textarea.value, "word");
  assert.equal(editor.textarea.focusCalls, 0);

  editor.textareaListeners.compositionend();
  assert.equal(editor.click(), 1);
  assert.equal(editor.textarea.value, "**word**");
  assert.equal(editor.textarea.focusCalls, 1);
});

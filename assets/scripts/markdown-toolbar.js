(() => {
  "use strict";

  const inlineActions = Object.freeze({
    bold: ["**", "text"],
    italic: ["*", "text"],
    strike: ["~~", "text"],
  });

  // Complexity: for n source bytes and m selected bytes, time and returned
  // space are O(n+m), Omega(n), and tight Theta(n+m); JavaScript strings are
  // immutable, so each accepted edit allocates the resulting source.
  function wrapInline(source, start, end, marker, placeholder) {
    const selected = source.slice(start, end);
    if (start >= marker.length && source.slice(start - marker.length, start) === marker && source.slice(end, end + marker.length) === marker) {
      return {
        value: source.slice(0, start - marker.length) + selected + source.slice(end + marker.length),
        start: start - marker.length,
        end: end - marker.length,
      };
    }
    if (selected.length >= marker.length * 2 && selected.startsWith(marker) && selected.endsWith(marker)) {
      const inner = selected.slice(marker.length, -marker.length);
      return {
        value: source.slice(0, start) + inner + source.slice(end),
        start,
        end: start + inner.length,
      };
    }
    const content = selected || placeholder;
    return {
      value: source.slice(0, start) + marker + content + marker + source.slice(end),
      start: start + marker.length,
      end: start + marker.length + content.length,
    };
  }

  // Complexity: for n source bytes across l touched lines, time and returned
  // space are O(n+l), Omega(n), and tight Theta(n+l); split/map/join own O(l)
  // temporary string headers in addition to the returned source.
  function prefixLines(source, start, end, matches, prefix) {
    const lineStart = source.lastIndexOf("\n", Math.max(0, start - 1)) + 1;
    const touchedEnd = end > start && source[end - 1] === "\n" ? end - 1 : end;
    const nextBreak = source.indexOf("\n", touchedEnd);
    const lineEnd = nextBreak === -1 ? source.length : nextBreak;
    const lines = source.slice(lineStart, lineEnd).split("\n");
    const removing = lines.every((line) => matches.test(line));
    const edits = [];
    let originalOffset = lineStart;
    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index];
      const match = removing ? line.match(matches) : null;
      edits.push({
        at: originalOffset,
        remove: match ? match[0].length : 0,
        insert: removing ? "" : prefix(index),
      });
      originalOffset += line.length + 1;
    }
    let changed = "";
    let copiedThrough = lineStart;
    for (const edit of edits) {
      changed += source.slice(copiedThrough, edit.at) + edit.insert;
      copiedThrough = edit.at + edit.remove;
    }
    changed += source.slice(copiedThrough, lineEnd);

    const mapped = (position) => {
      let delta = 0;
      for (const edit of edits) {
        if (position < edit.at) break;
        if (position <= edit.at + edit.remove) {
          return edit.at + delta + edit.insert.length;
        }
        delta += edit.insert.length - edit.remove;
      }
      return position + delta;
    };
    return {
      value: source.slice(0, lineStart) + changed + source.slice(lineEnd),
      start: mapped(start),
      end: mapped(end),
    };
  }

  function maximumBacktickRun(value) {
    let maximum = 0;
    let current = 0;
    for (const character of value) {
      if (character === "`") {
        current += 1;
        maximum = Math.max(maximum, current);
      } else {
        current = 0;
      }
    }
    return maximum;
  }

  // Inline code always uses padding around a nonblank selection. CommonMark
  // removes exactly that padding while preserving any spaces or backticks the
  // author selected. The delimiter is longer than every selected run.
  function inlineCode(source, start, end) {
    const selected = source.slice(start, end);
    if (start > 1 && source[start - 1] === " " && source[end] === " ") {
      let left = start - 2;
      while (left >= 0 && source[left] === "`") left -= 1;
      let right = end + 1;
      while (right < source.length && source[right] === "`") right += 1;
      const leftLength = start - 2 - left;
      const rightLength = right - end - 1;
      if (leftLength > 0 && leftLength === rightLength) {
        return {
          value: source.slice(0, left + 1) + selected + source.slice(right),
          start: left + 1,
          end: left + 1 + selected.length,
        };
      }
    }
    const content = selected || "code";
    const marker = "`".repeat(maximumBacktickRun(content) + 1);
    const inserted = marker + " " + content + " " + marker;
    return {
      value: source.slice(0, start) + inserted + source.slice(end),
      start: start + marker.length + 1,
      end: start + marker.length + 1 + content.length,
    };
  }

  // Complexity: for n source bytes and m selected bytes, time and returned
  // space are O(n+m), Omega(n), and tight Theta(n+m); no I/O or DOM work occurs.
  function fencedCode(source, start, end) {
    const selected = source.slice(start, end);
    if (start >= 4 && source[start - 1] === "\n" && source[end] === "\n") {
      let left = start - 2;
      while (left >= 0 && source[left] === "`") left -= 1;
      let right = end + 1;
      while (right < source.length && source[right] === "`") right += 1;
      const leftLength = start - 1 - (left + 1);
      const rightLength = right - (end + 1);
      if (leftLength >= 3 && leftLength === rightLength) {
        return {
          value: source.slice(0, left + 1) + selected + source.slice(right),
          start: left + 1,
          end: left + 1 + selected.length,
        };
      }
    }
    const content = selected || "code";
    const marker = "`".repeat(Math.max(3, maximumBacktickRun(content) + 1));
    return {
      value: source.slice(0, start) + marker + "\n" + content + "\n" + marker + source.slice(end),
      start: start + marker.length + 1,
      end: start + marker.length + 1 + content.length,
    };
  }

  // Complexity: for n source bytes and m selected bytes, time and returned
  // space are O(n+m), Omega(n), and tight Theta(n+m); immutable strings own the
  // result and no URL is fetched or interpreted.
  function linked(source, start, end) {
    const selected = source.slice(start, end);
    const label = selected || "link text";
    const destination = "https://example.org";
    const prefix = "[";
    const inserted = prefix + label + "](" + destination + ")";
    const selectionOffset = selected ? prefix.length + label.length + 2 : prefix.length;
    const selectionLength = selected ? destination.length : label.length;
    return {
      value: source.slice(0, start) + inserted + source.slice(end),
      start: start + selectionOffset,
      end: start + selectionOffset + selectionLength,
    };
  }

  // Complexity: for n source bytes and m selected bytes, time and returned
  // space are O(n+m), Omega(n), and tight Theta(n+m); the fixed table template
  // is bounded independently of input.
  function table(source, start, end) {
    const selected = source.slice(start, end) || "Column 1";
    const inserted = "| " + selected + " | Column 2 |\n| --- | --- |\n| Cell 1 | Cell 2 |";
    return {
      value: source.slice(0, start) + inserted + source.slice(end),
      start: start + 2,
      end: start + 2 + selected.length,
    };
  }

  // transform applies one closed toolbar action to a bounded textarea value
  // and returns the complete replacement plus deterministic selection.
  //
  // Complexity: for n source bytes and l touched lines, time and returned
  // space are O(n+l), Omega(n), with tight Theta(n+l) for line actions and
  // Theta(n) otherwise; delegated helpers own all allocations.
  function transform(source, start, end, action) {
    if (typeof source !== "string" || !Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start || end > source.length) {
      return { value: typeof source === "string" ? source : "", start: 0, end: 0 };
    }
    if (Object.hasOwn(inlineActions, action)) {
      return wrapInline(source, start, end, ...inlineActions[action]);
    }
    switch (action) {
      case "inline-code":
        return inlineCode(source, start, end);
      case "quote":
        return prefixLines(source, start, end, /^> /, () => "> ");
      case "unordered-list":
        return prefixLines(source, start, end, /^- /, () => "- ");
      case "ordered-list":
        return prefixLines(source, start, end, /^\d+\. /, (index) => String(index + 1) + ". ");
      case "task-list":
        return prefixLines(source, start, end, /^- \[[ xX]\] /, () => "- [ ] ");
      case "fenced-code":
        return fencedCode(source, start, end);
      case "link":
        return linked(source, start, end);
      case "table":
        return table(source, start, end);
      default:
        return { value: source, start, end };
    }
  }

  // enhance progressively wires every not-yet-enhanced editor beneath root.
  //
  // Complexity: for e editor roots and b buttons, time is O(e+b), Omega(e),
  // and tight Theta(e+b); auxiliary listener state is O(b), Omega(1), and
  // tight Theta(b). DOM query and listener costs are delegated to the browser.
  function enhance(root = document) {
    root.querySelectorAll("[data-markdown-editor]").forEach((editor) => {
      if (editor.dataset.markdownEnhanced === "true") return;
      const textarea = editor.querySelector("textarea");
      const toolbar = editor.querySelector("[data-markdown-toolbar]");
      if (!textarea || !toolbar) return;
      editor.querySelectorAll("[data-markdown-action]").forEach((button) => {
        button.addEventListener("click", (event) => {
          event.preventDefault();
          const result = transform(textarea.value, textarea.selectionStart, textarea.selectionEnd, button.dataset.markdownAction);
          textarea.value = result.value;
          textarea.focus();
          textarea.setSelectionRange(result.start, result.end);
        });
      });
      editor.dataset.markdownEnhanced = "true";
      toolbar.hidden = false;
    });
  }

  globalThis.gotthMarkdownToolbar = Object.freeze({ transform, enhance });
  enhance();
  document.addEventListener("htmx:afterSwap", (event) => enhance(event.target));
})();

(() => {
  "use strict";

  const inlineActions = Object.freeze({
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

  function adjacentStarRun(source, position, direction) {
    let cursor = position;
    if (direction < 0) {
      while (cursor > 0 && source[cursor - 1] === "*") cursor -= 1;
      return { start: cursor, end: position };
    }
    while (cursor < source.length && source[cursor] === "*") cursor += 1;
    return { start: position, end: cursor };
  }

  function escapedStars(length) {
    return "\\*".repeat(length);
  }

  function escapedStarRunBefore(source, position) {
    let cursor = position;
    while (cursor >= 2 && source[cursor - 2] === "\\" && source[cursor - 1] === "*") cursor -= 2;
    return { start: cursor, count: (position - cursor) / 2 };
  }

  function escapedStarRunAfter(source, position) {
    let cursor = position;
    while (cursor + 1 < source.length && source[cursor] === "\\" && source[cursor + 1] === "*") cursor += 2;
    return { end: cursor, count: (cursor - position) / 2 };
  }

  function removableStarRun(width, length) {
    return width === 1 ? length % 2 === 1 : length >= width;
  }

  function wrapStars(source, start, end, width) {
    const selected = source.slice(start, end);
    const marker = "*".repeat(width);
    if (start >= width && source.slice(start - width, start) === marker && source.slice(end, end + width) === marker) {
      const escapedLeft = escapedStarRunBefore(source, start - width);
      const escapedRight = escapedStarRunAfter(source, end + width);
      if (escapedLeft.count > 0 || escapedRight.count > 0) {
        const restored = "*".repeat(escapedLeft.count) + selected + "*".repeat(escapedRight.count);
        return {
          value: source.slice(0, escapedLeft.start) + restored + source.slice(escapedRight.end),
          start: escapedLeft.start + escapedLeft.count,
          end: escapedLeft.start + escapedLeft.count + selected.length,
        };
      }
      const left = adjacentStarRun(source, start, -1);
      const right = adjacentStarRun(source, end, 1);
      const leftLength = left.end - left.start;
      const rightLength = right.end - right.start;
      if (leftLength === rightLength && removableStarRun(width, leftLength)) {
        return {
          value: source.slice(0, start - width) + selected + source.slice(end + width),
          start: start - width,
          end: end - width,
        };
      }
    }

    let leading = 0;
    while (leading < selected.length && selected[leading] === "*") leading += 1;
    let trailing = 0;
    while (trailing < selected.length - leading && selected[selected.length - 1 - trailing] === "*") trailing += 1;
    if (leading === trailing && leading + trailing < selected.length && removableStarRun(width, leading)) {
      const inner = selected.slice(width, -width);
      return {
        value: source.slice(0, start) + inner + source.slice(end),
        start,
        end: start + inner.length,
      };
    }

    const content = selected || "text";
    const left = adjacentStarRun(source, start, -1);
    const right = adjacentStarRun(source, end, 1);
    const leftLength = left.end - left.start;
    const rightLength = right.end - right.start;
    if (leftLength === rightLength) {
      return {
        value: source.slice(0, start) + marker + content + marker + source.slice(end),
        start: start + width,
        end: start + width + content.length,
      };
    }
    const escapedLeft = escapedStars(leftLength);
    const escapedRight = escapedStars(rightLength);
    return {
      value: source.slice(0, left.start) + escapedLeft + marker + content + marker + escapedRight + source.slice(right.end),
      start: left.start + escapedLeft.length + width,
      end: left.start + escapedLeft.length + width + content.length,
    };
  }

  // Complexity: for n source bytes across l touched lines, time and returned
  // space are O(n+l), Omega(n), and tight Theta(n+l); split/map/join own O(l)
  // temporary string headers in addition to the returned source.
  function prefixLines(source, start, end, matches, prefix) {
    const lineStart = start === 0 ? 0 : source.lastIndexOf("\n", start - 1) + 1;
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

  function adjacentBacktickRun(source, position, direction) {
    let cursor = position;
    if (direction < 0) {
      while (cursor > 0 && source[cursor - 1] === "`") cursor -= 1;
      return { start: cursor, end: position };
    }
    while (cursor < source.length && source[cursor] === "`") cursor += 1;
    return { start: position, end: cursor };
  }

  function escapedBackticks(length) {
    return "\\`".repeat(length);
  }

  function escapedBacktickRunBefore(source, position) {
    let cursor = position;
    while (cursor >= 2 && source[cursor - 2] === "\\" && source[cursor - 1] === "`") cursor -= 2;
    return { start: cursor, count: (position - cursor) / 2 };
  }

  function escapedBacktickRunAfter(source, position) {
    let cursor = position;
    while (cursor + 1 < source.length && source[cursor] === "\\" && source[cursor + 1] === "`") cursor += 2;
    return { end: cursor, count: (cursor - position) / 2 };
  }

  function unwrapInlineCode(source, start, end, selected, padded) {
    const markerLength = maximumBacktickRun(selected) + 1;
    const leftMarkerEnd = padded ? start - 1 : start;
    const rightMarkerStart = padded ? end + 1 : end;
    if ((padded && (source[start - 1] !== " " || source[end] !== " ")) || leftMarkerEnd < markerLength) return null;
    const leftMarkerStart = leftMarkerEnd - markerLength;
    const rightMarkerEnd = rightMarkerStart + markerLength;
    const marker = "`".repeat(markerLength);
    if (source.slice(leftMarkerStart, leftMarkerEnd) !== marker || source.slice(rightMarkerStart, rightMarkerEnd) !== marker) return null;

    const escapedLeft = escapedBacktickRunBefore(source, leftMarkerStart);
    const escapedRight = escapedBacktickRunAfter(source, rightMarkerEnd);
    if ((source[leftMarkerStart - 1] === "`" && escapedLeft.count === 0) || source[rightMarkerEnd] === "`") return null;
    const replacement = "`".repeat(escapedLeft.count) + selected + "`".repeat(escapedRight.count);
    const replacementStart = escapedLeft.start;
    return {
      value: source.slice(0, escapedLeft.start) + replacement + source.slice(escapedRight.end),
      start: replacementStart + escapedLeft.count,
      end: replacementStart + escapedLeft.count + selected.length,
    };
  }

  // Inline code uses CommonMark padding except for all-space selections, where
  // padding would become content. The delimiter is longer than every selected
  // run. Adjacent unselected backticks are escaped while wrapped so Goldmark
  // sees complete delimiter runs, then restored byte-for-byte on toggle.
  function inlineCode(source, start, end) {
    const selected = source.slice(start, end);
    if (selected.length > 0 && selected.trim() === "") {
      const unwrapped = unwrapInlineCode(source, start, end, selected, false);
      if (unwrapped) return unwrapped;
      const marker = "`".repeat(maximumBacktickRun(selected) + 1);
      const left = adjacentBacktickRun(source, start, -1);
      const right = adjacentBacktickRun(source, end, 1);
      const escapedLeft = escapedBackticks(left.end - left.start);
      const escapedRight = escapedBackticks(right.end - right.start);
      return {
        value: source.slice(0, left.start) + escapedLeft + marker + selected + marker + escapedRight + source.slice(right.end),
        start: left.start + escapedLeft.length + marker.length,
        end: left.start + escapedLeft.length + marker.length + selected.length,
      };
    }
    const unwrapped = unwrapInlineCode(source, start, end, selected, true);
    if (unwrapped) return unwrapped;
    const content = selected || "code";
    const marker = "`".repeat(maximumBacktickRun(content) + 1);
    const left = adjacentBacktickRun(source, start, -1);
    const right = adjacentBacktickRun(source, end, 1);
    const escapedLeft = escapedBackticks(left.end - left.start);
    const escapedRight = escapedBackticks(right.end - right.start);
    const inserted = marker + " " + content + " " + marker;
    return {
      value: source.slice(0, left.start) + escapedLeft + inserted + escapedRight + source.slice(right.end),
      start: left.start + escapedLeft.length + marker.length + 1,
      end: left.start + escapedLeft.length + marker.length + 1 + content.length,
    };
  }

  function sourceLines(source) {
    const lines = [];
    let start = 0;
    while (start <= source.length) {
      const newline = source.indexOf("\n", start);
      if (newline === -1) {
        lines.push({ start, end: source.length, next: source.length, text: source.slice(start), eol: "" });
        break;
      }
      const hasCR = newline > start && source[newline - 1] === "\r";
      const end = hasCR ? newline - 1 : newline;
      lines.push({ start, end, next: newline + 1, text: source.slice(start, end), eol: hasCR ? "\r\n" : "\n" });
      start = newline + 1;
      if (start === source.length) {
        lines.push({ start, end: start, next: start, text: "", eol: "" });
        break;
      }
    }
    return lines;
  }

  function lineIndexAt(lines, position) {
    for (let index = 0; index < lines.length; index += 1) {
      if (position < lines[index].next || (position === lines[index].next && lines[index].next === lines[index].end)) return index;
    }
    return lines.length - 1;
  }

  function touchedLineRange(source, start, end) {
    const lines = sourceLines(source);
    const first = lineIndexAt(lines, start);
    const lastPosition = end > start ? end - 1 : end;
    return { lines, first, last: lineIndexAt(lines, lastPosition) };
  }

  function localLineEnding(lines, first, last) {
    for (let index = first; index <= last; index += 1) {
      if (lines[index].eol) return lines[index].eol;
    }
    if (first > 0 && lines[first - 1].eol) return lines[first - 1].eol;
    return "\n";
  }

  // Complexity: for n source bytes and m selected bytes, time and returned
  // space are O(n+m), Omega(n), and tight Theta(n+m); no I/O or DOM work occurs.
  function fencedCode(source, start, end) {
    const range = touchedLineRange(source, start, end);
    const firstLine = range.lines[range.first];
    const lastLine = range.lines[range.last];
    if (range.first > 0 && range.last + 1 < range.lines.length) {
      const opening = range.lines[range.first - 1];
      const closing = range.lines[range.last + 1];
      const openingMatch = opening.text.match(/^(`{3,})$/);
      const closingMatch = closing.text.match(/^(`{3,})$/);
      if (openingMatch && closingMatch && closingMatch[1].length >= openingMatch[1].length) {
        const content = source.slice(firstLine.start, lastLine.end);
        const restoredStart = opening.start;
        return {
          value: source.slice(0, opening.start) + content + source.slice(closing.end),
          start: restoredStart + (start - firstLine.start),
          end: restoredStart + (end - firstLine.start),
        };
      }
    }
    const original = source.slice(firstLine.start, lastLine.end);
    const content = original || "code";
    const eol = localLineEnding(range.lines, range.first, range.last);
    const marker = "`".repeat(Math.max(3, maximumBacktickRun(content) + 1));
    const inserted = marker + eol + content + eol + marker;
    const contentStart = firstLine.start + marker.length + eol.length;
    const mappedStart = original ? contentStart + (start - firstLine.start) : contentStart;
    const mappedEnd = original ? contentStart + (end - firstLine.start) : contentStart + content.length;
    return {
      value: source.slice(0, firstLine.start) + inserted + source.slice(lastLine.end),
      start: mappedStart,
      end: mappedEnd,
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

  function escapeTableCell(value) {
    return value.replaceAll("\\", "\\\\").replaceAll("|", "\\|");
  }

  function unescapeTableCell(value) {
    let unescaped = "";
    for (let index = 0; index < value.length; index += 1) {
      if (value[index] === "\\" && index + 1 < value.length && (value[index + 1] === "\\" || value[index + 1] === "|")) {
        index += 1;
      }
      unescaped += value[index];
    }
    return unescaped;
  }

  function tableCell(line, suffix) {
    const prefix = "| ";
    if (!line.text.startsWith(prefix) || !line.text.endsWith(suffix)) return null;
    const encoded = line.text.slice(prefix.length, -suffix.length);
    return {
      encoded,
      value: unescapeTableCell(encoded),
      start: line.start + prefix.length,
      end: line.end - suffix.length,
    };
  }

  function decodedPrefixLength(encoded, length) {
    return unescapeTableCell(encoded.slice(0, Math.max(0, Math.min(length, encoded.length)))).length;
  }

  function recognizedTable(lines, selectedFirst, selectedLast) {
    for (let headerIndex = selectedFirst; headerIndex >= 0; headerIndex -= 1) {
      if (headerIndex + 1 >= lines.length) continue;
      const header = tableCell(lines[headerIndex], " | Column 2 |");
      if (!header || !/^\| -{3,} \| -{3,} \|$/.test(lines[headerIndex + 1].text)) continue;
      const rows = [{ line: lines[headerIndex], cell: header }];
      let lastIndex = headerIndex + 1;
      for (let index = headerIndex + 2; index < lines.length; index += 1) {
        const cell = tableCell(lines[index], " | Cell 2 |");
        if (!cell) break;
        rows.push({ line: lines[index], cell });
        lastIndex = index;
      }
      if (selectedFirst < headerIndex || selectedLast > lastIndex || selectedFirst === headerIndex + 1) continue;
      return { headerIndex, lastIndex, rows };
    }
    return null;
  }

  // Complexity: for n source bytes and l touched lines, time and returned
  // space are O(n+l), Omega(n), and tight Theta(n+l). Existing text is escaped
  // into the first column; no Markdown delimiter carries hidden ownership.
  function table(source, start, end) {
    const range = touchedLineRange(source, start, end);
    const existing = recognizedTable(range.lines, range.first, range.last);
    if (existing) {
      const eol = localLineEnding(range.lines, existing.headerIndex, existing.lastIndex);
      const restored = existing.rows.map((row) => row.cell.value).join(eol);
      const restoredStart = range.lines[existing.headerIndex].start;
      const mapPosition = (position) => {
        let offset = 0;
        for (const row of existing.rows) {
          if (position <= row.cell.end) {
            return restoredStart + offset + decodedPrefixLength(row.cell.encoded, position - row.cell.start);
          }
          offset += row.cell.value.length + eol.length;
        }
        return restoredStart + restored.length;
      };
      return {
        value: source.slice(0, restoredStart) + restored + source.slice(range.lines[existing.lastIndex].end),
        start: mapPosition(start),
        end: mapPosition(end),
      };
    }
    const firstLine = range.lines[range.first];
    const lastLine = range.lines[range.last];
    const selectedLines = range.lines.slice(range.first, range.last + 1);
    const original = source.slice(firstLine.start, lastLine.end);
    const sourceRows = original ? selectedLines.map((line) => line.text) : ["Column 1"];
    const eol = localLineEnding(range.lines, range.first, range.last);
    const generated = [];
    const mappedRows = [];
    let generatedLength = 0;
    for (let index = 0; index < sourceRows.length; index += 1) {
      const escaped = escapeTableCell(sourceRows[index]);
      const prefix = "| ";
      const suffix = index === 0 ? " | Column 2 |" : " | Cell 2 |";
      const row = prefix + escaped + suffix;
      if (index === 1) {
        const separator = "| --- | --- |" + eol;
        generated.push(separator);
        generatedLength += separator.length;
      }
      mappedRows.push({ source: selectedLines[index], cellStart: generatedLength + prefix.length, escaped });
      generated.push(row);
      generatedLength += row.length;
      if (index + 1 < sourceRows.length) {
        generated.push(eol);
        generatedLength += eol.length;
      }
    }
    if (sourceRows.length === 1) {
      generated.push(eol + "| --- | --- |");
    }
    const inserted = generated.join("");
    const mapPosition = (position) => {
      if (!original) return firstLine.start + mappedRows[0].cellStart;
      for (let index = 0; index < mappedRows.length; index += 1) {
        const row = mappedRows[index];
        if (position <= row.source.end) {
          const column = Math.max(0, Math.min(position - row.source.start, row.source.text.length));
          return firstLine.start + row.cellStart + escapeTableCell(row.source.text.slice(0, column)).length;
        }
      }
      const last = mappedRows[mappedRows.length - 1];
      return firstLine.start + last.cellStart + last.escaped.length;
    };
    return {
      value: source.slice(0, firstLine.start) + inserted + source.slice(lastLine.end),
      start: mapPosition(start),
      end: original ? mapPosition(end) : mapPosition(start) + sourceRows[0].length,
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
    if (action === "bold" || action === "italic") {
      return wrapStars(source, start, end, action === "bold" ? 2 : 1);
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
      let composing = false;
      textarea.addEventListener("compositionstart", () => {
        composing = true;
      });
      textarea.addEventListener("compositionend", () => {
        composing = false;
      });
      editor.querySelectorAll("[data-markdown-action]").forEach((button) => {
        let suppressPointerClick = false;
        const guardPointerDown = (event) => {
          if (!composing) {
            suppressPointerClick = false;
            return;
          }
          suppressPointerClick = true;
          event.preventDefault();
        };
        const guardMouseDown = (event) => {
          if (!composing) return;
          suppressPointerClick = true;
          event.preventDefault();
        };
        button.addEventListener("pointerdown", guardPointerDown);
        button.addEventListener("mousedown", guardMouseDown);
        button.addEventListener("click", (event) => {
          event.preventDefault();
          if (composing || suppressPointerClick) {
            suppressPointerClick = false;
            return;
          }
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

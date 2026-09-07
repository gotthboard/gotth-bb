(() => {
  "use strict";

  const marker = "X-GOTTH-Discovery-Response";
  const swappableErrors = new Set([400, 404, 503]);

  document.addEventListener("htmx:beforeSwap", (event) => {
    const detail = event.detail;
    const xhr = detail && detail.xhr;
    if (!xhr || xhr.getResponseHeader(marker) !== "1" || !swappableErrors.has(xhr.status)) {
      return;
    }
    detail.shouldSwap = true;
    detail.isError = true;
  });
})();

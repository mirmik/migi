document.addEventListener("DOMContentLoaded", () => {
  const message = document.querySelector("[data-agent-message]");
  if (!message || typeof renderMathInElement !== "function") return;
  renderMathInElement(message, {
    delimiters: [
      {left: "$$", right: "$$", display: true},
      {left: "\\[", right: "\\]", display: true},
      {left: "\\(", right: "\\)", display: false},
      {left: "$", right: "$", display: false}
    ],
    ignoredTags: ["script", "noscript", "style", "textarea", "pre", "code"],
    throwOnError: false,
    trust: false
  });
});

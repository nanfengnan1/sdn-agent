/* Progressive enhancement; navigation and commands remain usable without JS. */
(function () {
  "use strict";

  function fallbackCopy(text) {
    const active = document.activeElement;
    const field = document.createElement("textarea");
    field.value = text;
    field.readOnly = true;
    field.tabIndex = -1;
    field.style.cssText = "position:fixed;top:0;left:0;opacity:0;pointer-events:none";
    document.body.appendChild(field);
    try {
      field.select();
      return document.execCommand("copy");
    } catch (_) {
      return false;
    } finally {
      field.remove();
      if (active && typeof active.focus === "function") active.focus({ preventScroll: true });
    }
  }

  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch (_) { /* file:// and denied permissions may require the fallback. */ }
    }
    return fallbackCopy(text);
  }

  function install() {
    document.querySelectorAll(".rst-content .highlight").forEach(function (block) {
      const pre = block.querySelector("pre");
      if (!pre || block.querySelector(".sdn-code-tools")) return;
      const tools = document.createElement("div");
      tools.className = "sdn-code-tools";
      const label = document.createElement("span");
      label.className = "sdn-code-label";
      const caption = block.closest(".literal-block-wrapper");
      const captionText = caption && caption.querySelector(".caption-text");
      label.textContent = captionText ? captionText.textContent : "代码 / 示例";
      if (captionText) caption.classList.add("sdn-code-captioned");
      const button = document.createElement("button");
      button.type = "button";
      button.className = "sdn-copy";
      button.textContent = "复制";
      button.setAttribute("aria-label", "复制代码块");
      const status = document.createElement("span");
      status.className = "sdn-copy-status";
      status.setAttribute("role", "status");
      let reset;
      let copying = false;
      button.addEventListener("click", async function () {
        if (copying) return;
        copying = true;
        clearTimeout(reset);
        button.setAttribute("aria-busy", "true");
        status.textContent = "";
        const code = pre.cloneNode(true);
        code.querySelectorAll(".linenos").forEach(function (line) { line.remove(); });
        const copied = await copyText(code.textContent);
        copying = false;
        button.removeAttribute("aria-busy");
        if (!copied) {
          const selection = window.getSelection();
          const range = document.createRange();
          range.selectNodeContents(pre);
          selection.removeAllRanges();
          selection.addRange(range);
        }
        button.textContent = copied ? "已复制" : "请手动复制";
        status.textContent = copied ? "代码已复制到剪贴板" : "自动复制不可用，已选中代码，请按 Ctrl+C 或 Command+C 复制";
        reset = setTimeout(function () { button.textContent = "复制"; }, 2500);
      });
      tools.append(label, button, status);
      block.insertBefore(tools, pre);
    });

    const outline = document.querySelector(".sdn-doc-outline");
    if (!outline) return;
    const entries = Array.from(outline.querySelectorAll('a[href^="#"]')).map(function (link) {
      const id = decodeURIComponent(link.hash.slice(1));
      return { link: link, target: id && document.getElementById(id) };
    }).filter(function (entry) { return entry.target; });
    let queued = false;
    function update() {
      queued = false;
      let current = null;
      entries.forEach(function (entry) {
        if (entry.link.getClientRects().length && entry.target.getBoundingClientRect().top <= 64) current = entry;
        entry.link.removeAttribute("aria-current");
      });
      if (current) current.link.setAttribute("aria-current", "location");
    }
    window.addEventListener("scroll", function () {
      if (!queued) { queued = true; requestAnimationFrame(update); }
    }, { passive: true });
    update();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", install);
  else install();
}());

// Click-to-copy for values the user must not mistype: the one-shot token page
// shows a probe ID, a token and a push endpoint, and all three end up in a
// config file by hand.
//
// A clickable element carries data-copy="<selector>"; the selector names the
// element whose text is the value. Reading the value out of the DOM instead of
// out of an attribute keeps the secret in one place in the markup and
// guarantees the bytes copied are exactly the bytes on screen.
(function () {
  "use strict";

  var RESET_MS = 1500;

  function copy(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return legacyCopy(text);
  }

  // navigator.clipboard only exists in a secure context. The gateway is meant to
  // be reached over HTTPS, but a LAN deployment opened by IP is not one, and the
  // button has to work there too.
  function legacyCopy(text) {
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.top = "-1000px";
    document.body.appendChild(area);
    area.select();
    var copied = false;
    try {
      copied = document.execCommand("copy");
    } catch (err) {
      copied = false;
    }
    document.body.removeChild(area);
    return copied ? Promise.resolve() : Promise.reject(new Error("copy failed"));
  }

  // select highlights a value so the user can copy it by hand. It is the
  // fallback for a blocked clipboard: the message alone would leave them to
  // select a 53-character token by dragging.
  function select(element) {
    var selection = window.getSelection();
    if (!selection) {
      return;
    }
    var range = document.createRange();
    range.selectNodeContents(element);
    selection.removeAllRanges();
    selection.addRange(range);
  }

  // flash swaps the button's label for a result message, then puts it back. The
  // label sits in an aria-live region, so the outcome is announced as well as
  // shown. The stylesheet reserves the width of the longest message, so the
  // value beside it never reflows.
  function flash(button, message, state) {
    if (button.dataset.busy === "1") {
      return;
    }
    button.dataset.busy = "1";
    var original = button.textContent;
    button.textContent = message;
    button.classList.add(state);
    window.setTimeout(function () {
      button.textContent = original;
      button.dataset.busy = "";
      button.classList.remove(state);
    }, RESET_MS);
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy]");
    if (!button) {
      return;
    }
    var source = document.querySelector(button.getAttribute("data-copy"));
    if (!source) {
      return;
    }
    copy(source.textContent.trim()).then(
      function () {
        flash(button, "已复制 ✓", "copied");
      },
      function () {
        select(source);
        flash(button, "复制失败", "failed");
      }
    );
  });
})();

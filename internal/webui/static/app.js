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

// Custom dropdowns.
//
// The open list of a native <select> is painted by the operating system and no
// stylesheet can reach it, which is why a native select looks like a native
// select no matter what the closed control looks like. So every <select> inside
// a .select wrapper is augmented with a listbox we draw ourselves, following
// the ARIA select-only combobox pattern.
//
// The native element stays in the DOM, hidden, and remains the value carrier:
// it keeps its name and its place in the form, so submission and form.reset()
// need no knowledge of any of this. Without scripting none of this runs and the
// native select stays visible — the .ready class added here is what hides it.
(function () {
  "use strict";

  var TYPEAHEAD_MS = 1000;
  var instances = [];
  var seq = 0;

  // labelFor reads the field caption from the wrapping <label>, minus the
  // control itself: "校区" from <label>校区 <div class="select">…</div></label>.
  // It becomes the accessible name of a control that is no longer the one the
  // label points at.
  function labelFor(select) {
    var label = select.closest("label");
    if (!label) {
      return select.name || "";
    }
    var copy = label.cloneNode(true);
    var wrapper = copy.querySelector(".select") || copy.querySelector("select");
    if (wrapper) {
      wrapper.remove();
    }
    return copy.textContent.replace(/\s+/g, " ").trim();
  }

  function enhance(wrapper) {
    var select = wrapper.querySelector("select");
    if (!select || wrapper.classList.contains("ready")) {
      return;
    }

    var id = "select-" + ++seq;
    var name = labelFor(select);
    var options = Array.prototype.map.call(select.options, function (option) {
      return {
        label: option.textContent.replace(/\s+/g, " ").trim(),
        disabled: option.disabled
      };
    });

    var trigger = document.createElement("button");
    trigger.type = "button";
    trigger.className = "select-trigger";
    trigger.setAttribute("role", "combobox");
    trigger.setAttribute("aria-haspopup", "listbox");
    trigger.setAttribute("aria-expanded", "false");
    trigger.setAttribute("aria-controls", id);
    if (name) {
      trigger.setAttribute("aria-label", name);
    }
    if (select.required) {
      trigger.setAttribute("aria-required", "true");
    }
    trigger.disabled = select.disabled;

    var value = document.createElement("span");
    value.className = "select-value";
    var arrow = document.createElement("span");
    arrow.className = "select-arrow";
    arrow.setAttribute("aria-hidden", "true");
    arrow.textContent = "▾";
    trigger.appendChild(value);
    trigger.appendChild(arrow);

    var menu = document.createElement("ul");
    menu.className = "select-menu";
    menu.id = id;
    menu.setAttribute("role", "listbox");
    if (name) {
      menu.setAttribute("aria-label", name);
    }
    menu.hidden = true;

    var items = options.map(function (option, i) {
      var li = document.createElement("li");
      li.className = "select-option";
      li.id = id + "-option-" + i;
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", "false");
      if (option.disabled) {
        li.setAttribute("aria-disabled", "true");
        li.classList.add("disabled");
      }
      li.textContent = option.label;
      menu.appendChild(li);
      return li;
    });

    wrapper.appendChild(trigger);
    wrapper.appendChild(menu);

    var active = 0;
    var typed = "";
    var typedAt = 0;

    function sync() {
      var index = select.selectedIndex;
      value.textContent = index >= 0 ? options[index].label : "";
      value.classList.toggle("placeholder", select.value === "");
      items.forEach(function (li, i) {
        li.setAttribute("aria-selected", i === index ? "true" : "false");
      });
      wrapper.classList.remove("invalid");
      trigger.removeAttribute("aria-invalid");
    }

    function setActive(index) {
      if (index < 0) {
        index = 0;
      }
      if (index >= items.length) {
        index = items.length - 1;
      }
      if (index < 0) {
        return;
      }
      active = index;
      items.forEach(function (li, i) {
        li.classList.toggle("active", i === index);
      });
      trigger.setAttribute("aria-activedescendant", items[index].id);
      items[index].scrollIntoView({ block: "nearest" });
    }

    function open() {
      if (!menu.hidden) {
        return;
      }
      menu.hidden = false;
      trigger.setAttribute("aria-expanded", "true");
      // A list that would run past the bottom of the window opens upwards
      // instead, but only when there is more room up there than down.
      var triggerBox = trigger.getBoundingClientRect();
      var below = window.innerHeight - triggerBox.bottom;
      wrapper.classList.toggle("drop-up", menu.offsetHeight > below && triggerBox.top > below);
      setActive(select.selectedIndex >= 0 ? select.selectedIndex : 0);
    }

    function close() {
      if (menu.hidden) {
        return;
      }
      menu.hidden = true;
      trigger.setAttribute("aria-expanded", "false");
      trigger.removeAttribute("aria-activedescendant");
      wrapper.classList.remove("drop-up");
    }

    function choose(index) {
      if (index < 0 || index >= options.length || options[index].disabled) {
        return;
      }
      if (select.selectedIndex !== index) {
        select.selectedIndex = index;
        // A bubbling change keeps any inline onchange on the wrapper working:
        // the handler is attached to the wrapper precisely so it fires for both
        // the native control and this one.
        select.dispatchEvent(new Event("change", { bubbles: true }));
      }
      sync();
      close();
      trigger.focus();
    }

    function typeahead(character) {
      var now = Date.now();
      typed = (now - typedAt < TYPEAHEAD_MS ? typed : "") + character.toLowerCase();
      typedAt = now;
      var start = (menu.hidden ? select.selectedIndex : active) + 1;
      for (var offset = 0; offset < items.length; offset++) {
        var i = (start + offset + items.length) % items.length;
        if (options[i].label.toLowerCase().indexOf(typed) === 0) {
          open();
          setActive(i);
          return;
        }
      }
    }

    trigger.addEventListener("click", function () {
      if (menu.hidden) {
        open();
      } else {
        close();
      }
    });

    trigger.addEventListener("keydown", function (event) {
      switch (event.key) {
        case "ArrowDown":
          event.preventDefault();
          if (menu.hidden) {
            open();
          } else {
            setActive(active + 1);
          }
          break;
        case "ArrowUp":
          event.preventDefault();
          if (menu.hidden) {
            open();
          } else {
            setActive(active - 1);
          }
          break;
        case "Home":
          if (!menu.hidden) {
            event.preventDefault();
            setActive(0);
          }
          break;
        case "End":
          if (!menu.hidden) {
            event.preventDefault();
            setActive(items.length - 1);
          }
          break;
        case "Enter":
        case " ":
          // preventDefault also suppresses the click this key would fire on a
          // button, which would otherwise toggle the menu straight back shut.
          event.preventDefault();
          if (menu.hidden) {
            open();
          } else {
            choose(active);
          }
          break;
        case "Escape":
          if (!menu.hidden) {
            event.preventDefault();
            close();
          }
          break;
        case "Tab":
          close();
          break;
        default:
          if (event.key.length === 1) {
            typeahead(event.key);
          }
      }
    });

    // mousedown is prevented so the trigger keeps focus and the click below
    // still lands on the option the pointer went down on.
    menu.addEventListener("mousedown", function (event) {
      event.preventDefault();
    });
    menu.addEventListener("click", function (event) {
      var li = event.target.closest(".select-option");
      if (li) {
        choose(items.indexOf(li));
      }
    });

    // A value set from elsewhere (a script, autofill) must show up here too.
    select.addEventListener("change", sync);

    wrapper.classList.add("ready");
    sync();

    instances.push({
      wrapper: wrapper,
      trigger: trigger,
      select: select,
      required: select.required,
      close: close
    });
  }

  document.querySelectorAll(".select > select").forEach(function (select) {
    enhance(select.parentNode);
  });

  document.addEventListener("mousedown", function (event) {
    instances.forEach(function (instance) {
      if (!instance.wrapper.contains(event.target)) {
        instance.close();
      }
    });
  });

  // A required select is hidden once enhanced, and a hidden control is barred
  // from the browser's own constraint validation — so the check is repeated
  // here rather than silently lost. The server validates the same fields
  // anyway; this only saves a round trip and a lost form fill.
  document.addEventListener("submit", function (event) {
    var firstBad = null;
    instances.forEach(function (instance) {
      if (!instance.required || instance.select.value !== "") {
        return;
      }
      instance.wrapper.classList.add("invalid");
      instance.trigger.setAttribute("aria-invalid", "true");
      if (!firstBad) {
        firstBad = instance;
      }
    });
    if (firstBad) {
      event.preventDefault();
      firstBad.trigger.focus();
    }
  });
})();

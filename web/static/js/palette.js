/* The command palette (docs/03 "Command palette", docs/22): Ctrl/Cmd+K or
 * the top bar's Commands button opens it, Escape or Ctrl/Cmd+K again
 * closes it. It is a combobox: focus stays in the input, ↑/↓ move the
 * active option (aria-activedescendant, aria-selected), Enter chooses it.
 *
 * The commands are rendered by the server (templates/palette.templ) and
 * filtered here by their label; the monitor group is filled by htmx from
 * /fragments/palette as you type. Choosing an option clicks it: links swap
 * the pane and Pause/Resume submit the palette's form, both boosted by the
 * shell; theme options are saved to /account/appearance here and applied
 * to the open page at once. */
(function () {
  "use strict";

  var dialog = function () { return document.querySelector("[data-palette]"); };
  var input = function (d) { return d.querySelector("[role='combobox']"); };
  var opener = null;

  function shown(d) {
    return Array.prototype.filter.call(d.querySelectorAll("[role='option']"), function (o) {
      return !o.closest("li").hidden;
    });
  }

  function setActive(d, opt) {
    Array.prototype.forEach.call(d.querySelectorAll("[role='option'][aria-selected]"), function (o) {
      o.removeAttribute("aria-selected");
    });
    var box = input(d);
    if (!opt) {
      box.removeAttribute("aria-activedescendant");
      return;
    }
    opt.setAttribute("aria-selected", "true");
    box.setAttribute("aria-activedescendant", opt.id);
    opt.scrollIntoView({ block: "nearest" });
  }

  function note(d, text) {
    d.querySelector("[data-palette-note]").textContent = text;
  }

  // Hide the commands whose label does not contain the text; the monitor
  // group is already filtered by the server. A group without a shown
  // option is hidden with its heading. The first option becomes active.
  function filter(d) {
    var text = input(d).value.trim().toLowerCase();
    Array.prototype.forEach.call(d.querySelectorAll(".palette-group"), function (g) {
      var any = false;
      Array.prototype.forEach.call(g.querySelectorAll("[role='option']"), function (o) {
        var li = o.closest("li");
        if (g.id !== "palette-monitors") {
          li.hidden = text !== "" && o.querySelector(".palette-label").textContent.toLowerCase().indexOf(text) < 0;
        }
        any = any || !li.hidden;
      });
      g.hidden = !any;
    });
    var opts = shown(d);
    note(d, opts.length ? "" : "Nothing matches.");
    setActive(d, opts[0]);
  }

  function open() {
    var d = dialog();
    if (!d || d.open) {
      return;
    }
    opener = document.activeElement;
    input(d).value = "";
    d.querySelector("#palette-monitors").replaceChildren();
    filter(d);
    d.showModal();
    input(d).focus();
  }

  function close() {
    var d = dialog();
    if (d && d.open) {
      d.close();
    }
  }

  // --- Theme ---
  function saveTheme(d, value) {
    var html = document.documentElement;
    var body = new URLSearchParams();
    body.set("_csrf", d.querySelector("input[name='_csrf']").value);
    body.set("theme", value);
    body.set("density", html.getAttribute("data-density"));
    if (html.getAttribute("data-ambient") === "on") {
      body.set("ambient", "on");
    }
    // A saved form answers 303 to the appearance page, which is not needed.
    fetch("/account/appearance", { method: "POST", body: body, credentials: "same-origin", redirect: "manual" })
      .then(function (res) {
        if (res.type !== "opaqueredirect" && !res.ok) {
          throw new Error(res.status);
        }
        html.setAttribute("data-theme", value);
        var meta = document.querySelector('meta[name="color-scheme"]');
        if (meta) {
          meta.setAttribute("content", value === "daylight" ? "light" : "dark");
        }
        try { // what the pre-hydration script remembers (docs/05)
          window.localStorage.setItem("sinjal.prefs", JSON.stringify([value, html.getAttribute("data-density"), html.getAttribute("data-ambient")]));
        } catch (e) { /* storage unavailable */ }
        close();
      })
      .catch(function () {
        note(d, "The theme could not be saved. Try again, or use Your appearance.");
      });
  }

  // --- Opening and closing ---
  document.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "k" || e.key === "K")) {
      if (!dialog()) {
        return;
      }
      e.preventDefault();
      if (dialog().open) {
        close();
      } else {
        open();
      }
    }
  });

  document.addEventListener("click", function (e) {
    if (e.target.closest && e.target.closest("[data-palette-open]")) {
      open();
    }
  });

  var d = dialog();
  if (!d) {
    return;
  }
  var button = document.querySelector("[data-palette-open]");
  if (button) {
    button.hidden = false;
    if (/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) {
      button.querySelector("[data-palette-kbd]").textContent = "⌘ K";
    }
  }

  d.addEventListener("close", function () {
    if (opener && opener.isConnected && opener.focus) {
      opener.focus();
    }
    opener = null;
  });

  // --- Inside the palette ---
  d.addEventListener("input", function (e) {
    if (e.target === input(d)) {
      filter(d);
    }
  });

  d.addEventListener("keydown", function (e) {
    if (e.target !== input(d)) {
      return;
    }
    var opts = shown(d);
    var i = opts.indexOf(d.querySelector("[role='option'][aria-selected='true']"));
    switch (e.key) {
    case "ArrowDown":
      e.preventDefault();
      setActive(d, opts[(i + 1) % opts.length]);
      break;
    case "ArrowUp":
      e.preventDefault();
      setActive(d, opts[(i - 1 + opts.length) % opts.length]);
      break;
    case "Enter":
      e.preventDefault();
      if (opts[i]) {
        opts[i].click();
      }
      break;
    }
  });

  d.addEventListener("mousemove", function (e) {
    var opt = e.target.closest("[role='option']");
    if (opt && opt.getAttribute("aria-selected") !== "true") {
      setActive(d, opt);
    }
  });

  d.addEventListener("click", function (e) {
    if (e.target === d) { // the backdrop
      close();
      return;
    }
    var opt = e.target.closest("[role='option']");
    if (!opt) {
      return;
    }
    var theme = opt.getAttribute("data-palette-theme");
    if (theme) {
      saveTheme(d, theme);
      return;
    }
    // A link or a Pause/Resume button: htmx sends it in this same click
    // (a button's formaction is read while it is the clicked one), so the
    // palette closes after, keeping focus where htmx expects it.
    setTimeout(close, 0);
  });

  // New monitor results: the first option is active again.
  d.addEventListener("htmx:afterSwap", function () {
    filter(d);
  });
})();

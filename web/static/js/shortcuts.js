/* Keyboard shortcuts (docs/03 "Keyboard shortcuts"): "/" focuses the
 * column's search, "g" then "d", "m" or "i" goes to Home, Monitors or
 * Incidents, "n" opens Add monitor. Ctrl/Cmd+K belongs to palette.js.
 *
 * Nothing fires while you type (an input, textarea, select or editable
 * element has focus), while a dialog is open, or with Ctrl/Cmd/Alt held.
 * "n" only exists for admins: it follows the palette's own Add monitor
 * link, which the server renders for them alone. Navigation clicks a link
 * made inside the shell, so hx-boost swaps the pane as it does for any
 * other link. */
(function () {
  "use strict";

  var GO = { d: "/", m: "/monitors", i: "/incidents" };
  var CHORD_MS = 1500;
  var armed = 0;

  function typing(el) {
    if (!el || !el.tagName) {
      return false;
    }
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
  }

  function go(href) {
    var shell = document.querySelector(".shell");
    if (!shell) {
      window.location.assign(href);
      return;
    }
    var a = document.createElement("a");
    a.href = href;
    a.hidden = true;
    shell.appendChild(a);
    a.click();
    shell.removeChild(a);
  }

  document.addEventListener("keydown", function (e) {
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
      return;
    }
    if (typing(e.target) || document.querySelector("dialog[open]")) {
      armed = 0;
      return;
    }
    var key = e.key.length === 1 ? e.key.toLowerCase() : "";
    var chord = armed && Date.now() - armed < CHORD_MS;
    armed = 0;
    if (!key) {
      return;
    }
    if (chord && GO[key]) {
      e.preventDefault();
      go(GO[key]);
      return;
    }
    if (key === "g") {
      armed = Date.now();
    } else if (key === "/") {
      var search = document.getElementById("column-search");
      if (search) {
        e.preventDefault();
        search.focus();
        search.select();
      }
    } else if (key === "n") {
      var add = document.querySelector("[data-palette] a[href='/monitors/new']");
      if (add) {
        e.preventDefault();
        go("/monitors/new");
      }
    }
  });
})();

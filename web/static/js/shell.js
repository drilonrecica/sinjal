/* The app shell (docs/03 "Layout"): what the monitor list column and the
 * Live activity column need besides htmx. It renders nothing; it marks,
 * hides and asks for fragments.
 *
 * Column: the row of the monitor in the pane is marked (aria-current) when
 * the page loads and after every boosted navigation; ↑/↓, Home and End move
 * between rows; the search box and the chips hide rows that do not match
 * (data-search, data-state). Rows that refresh from the event stream keep
 * the current search and mark.
 *
 * Live activity: loaded only while the column is shown (from 2200 px), and
 * again on monitor and incident events, at most once every 3 s.
 *
 * Menus: the top bar's menu is open by default (it is the navigation
 * without JavaScript) and closed here on phones; both menus close after a
 * navigation.
 *
 * Navigation: links and forms are boosted; the server aims the answer at
 * #main (internal/web/boost.go). An answer without a #main (sign-in,
 * re-authentication, a public status page) is not swapped in: the browser
 * loads it as a page. After a swap the current section is marked. */
(function () {
  "use strict";

  var column = function () { return document.querySelector(".monitor-column"); };

  // --- The row of the monitor in the pane ---
  function markCurrent() {
    var col = column();
    if (!col) {
      return;
    }
    var m = location.pathname.match(/^\/monitors\/([0-9a-f]+)/);
    Array.prototype.forEach.call(col.querySelectorAll(".column-link"), function (a) {
      var row = a.closest(".column-row");
      if (m && row && row.getAttribute("data-monitor-id") === m[1]) {
        a.setAttribute("aria-current", "page");
      } else {
        a.removeAttribute("aria-current");
      }
    });
  }

  // --- Search and chips ---
  var filter = { text: "", state: "" };

  function applyFilter() {
    var col = column();
    if (!col) {
      return;
    }
    Array.prototype.forEach.call(col.querySelectorAll(".column-row"), function (row) {
      var okText = !filter.text || (row.getAttribute("data-search") || "").indexOf(filter.text) >= 0;
      var okState = !filter.state || row.getAttribute("data-state") === filter.state;
      row.hidden = !(okText && okState);
    });
  }

  document.addEventListener("input", function (e) {
    if (e.target.matches && e.target.matches("[data-column-search]")) {
      filter.text = e.target.value.trim().toLowerCase();
      applyFilter();
    }
  });

  document.addEventListener("click", function (e) {
    var chip = e.target.closest && e.target.closest("[data-column-filter]");
    if (!chip) {
      return;
    }
    filter.state = chip.getAttribute("data-column-filter");
    Array.prototype.forEach.call(document.querySelectorAll("[data-column-filter]"), function (c) {
      c.setAttribute("aria-pressed", c === chip ? "true" : "false");
    });
    applyFilter();
  });

  // --- Keyboard in the column ---
  document.addEventListener("keydown", function (e) {
    var link = e.target.closest && e.target.closest(".column-link");
    if (!link || e.altKey || e.ctrlKey || e.metaKey) {
      return;
    }
    var links = Array.prototype.filter.call(column().querySelectorAll(".column-link"), function (a) {
      return !a.closest(".column-row").hidden;
    });
    var i = links.indexOf(link);
    var next = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: links.length - 1 }[e.key];
    if (next === undefined || !links[next]) {
      return;
    }
    e.preventDefault();
    links[next].focus();
  });

  // --- Live activity ---
  var wide = window.matchMedia("(min-width: 2200px)");
  var last = 0;
  var pending = null;

  function loadActivity() {
    var el = document.querySelector("[data-activity]");
    if (!el || !wide.matches) {
      return;
    }
    var wait = last + 3000 - Date.now();
    if (wait > 0) {
      if (!pending) {
        pending = setTimeout(function () { pending = null; loadActivity(); }, wait);
      }
      return;
    }
    last = Date.now();
    htmx.trigger(el, "activity");
  }

  ["monitor.updated", "incident.opened", "incident.updated", "incident.closed"].forEach(function (name) {
    document.addEventListener("sse:" + name, loadActivity);
  });
  wide.addEventListener("change", loadActivity);

  // --- Menus ---
  function closeMenus() {
    var phone = window.matchMedia("(max-width: 47.9375rem)").matches;
    Array.prototype.forEach.call(document.querySelectorAll("[data-topbar-menu]"), function (d) {
      d.open = !phone;
    });
    Array.prototype.forEach.call(document.querySelectorAll(".account-menu"), function (d) {
      d.open = false;
    });
  }

  // --- The current section in the top bar ---
  function markSection() {
    var path = location.pathname;
    Array.prototype.forEach.call(document.querySelectorAll("#topnav .nav-link"), function (a) {
      var href = a.getAttribute("href");
      var base = href.split("/").slice(0, 2).join("/"); // "/settings/general" → "/settings"
      if (path === href || path.indexOf(base + "/") === 0 || path === base) {
        a.setAttribute("aria-current", "page");
      } else {
        a.removeAttribute("aria-current");
      }
    });
  }

  // --- Boosted answers without a pane ---
  document.addEventListener("htmx:beforeSwap", function (e) {
    var cfg = e.detail.requestConfig;
    if (!cfg || !cfg.boosted) {
      return;
    }
    var text = e.detail.xhr && e.detail.xhr.responseText;
    if (text && text.indexOf('id="main"') < 0) {
      e.detail.shouldSwap = false;
      location.assign(e.detail.xhr.responseURL || cfg.path);
    }
  });

  // --- When things change ---
  document.addEventListener("htmx:afterSettle", function (e) {
    if (e.target.closest && e.target.closest(".monitor-column")) {
      markCurrent();
      applyFilter();
    }
  });
  function navigated() {
    // Back and forward put another page into the same #main: its phone
    // view follows the address (docs/03 "Phones").
    var main = document.getElementById("main");
    if (main) {
      main.setAttribute("data-view", location.pathname === "/monitors" ? "list" : "detail");
    }
    markCurrent();
    markSection();
    closeMenus();
  }
  document.addEventListener("htmx:pushedIntoHistory", navigated);
  document.addEventListener("htmx:historyRestore", navigated);
  window.addEventListener("popstate", navigated);

  markCurrent();
  closeMenus();
  // Deferred scripts run before htmx sets up its elements (on
  // DOMContentLoaded, its listener added first): ask for Live activity after.
  document.addEventListener("DOMContentLoaded", function () {
    setTimeout(loadActivity, 0);
  });
})();

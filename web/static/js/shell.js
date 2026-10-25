/* The app shell (docs/03 "Layout"): what the monitor list column and the
 * Live activity column need besides htmx. It renders nothing; it marks,
 * hides and asks for fragments.
 *
 * Column: the row of the monitor in the pane is marked (aria-current) when
 * the page loads and after every boosted navigation; ↑/↓, Home and End move
 * between rows. The search box, the chips and the More panel are one form
 * the server answers with the filtered list (docs/29); rows that refresh
 * from the event stream keep the mark.
 *
 * Live activity: loaded only while the column is shown (from 2200 px), and
 * again on monitor and incident events, at most once every 3 s.
 *
 * Menus: the top bar's menu is open by default (it is the navigation
 * without JavaScript) and closed here on phones, where it opens as a panel
 * over the page; both menus close after a navigation, on Escape and on a
 * click elsewhere.
 *
 * Tabs: on phones a row of tabs scrolls; its current tab is scrolled into
 * view and its edge fades while more tabs are hidden.
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

  // --- Chips ---
  // The column's form asks the server for the list (hx-get on the form):
  // typing and the More panel's fields do it by themselves. A chip sets the
  // form's hidden "state" field and asks the same way.
  document.addEventListener("click", function (e) {
    var chip = e.target.closest && e.target.closest("[data-column-filter]");
    if (!chip) {
      return;
    }
    var form = chip.closest("form");
    form.elements.state.value = chip.getAttribute("data-column-filter");
    Array.prototype.forEach.call(form.querySelectorAll("[data-column-filter]"), function (c) {
      c.setAttribute("aria-pressed", c === chip ? "true" : "false");
    });
    htmx.trigger(form, "change");
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

  // A click outside a menu, or Escape, closes it (on phones the top bar's
  // menu is a panel over the page).
  document.addEventListener("click", function (e) {
    if (!(e.target.closest && e.target.closest("[data-topbar-menu], .account-menu"))) {
      closeMenus();
    }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") {
      return;
    }
    // On larger screens the top bar's menu is the open nav itself.
    var open = document.querySelector(".account-menu[open]") ||
      (window.matchMedia("(max-width: 47.9375rem)").matches && document.querySelector("[data-topbar-menu][open]"));
    if (open) {
      closeMenus();
      open.querySelector("summary").focus();
    }
  });

  // --- Tabs on phones ---
  // A row of tabs scrolls sideways on phones (components.css): it shows
  // its current tab, and data-more fades its right edge while tabs are
  // hidden past it.
  function tabMore(ul) {
    if (ul.scrollLeft + ul.clientWidth < ul.scrollWidth - 1) {
      ul.setAttribute("data-more", "");
    } else {
      ul.removeAttribute("data-more");
    }
  }
  function tabRows(root) {
    var rows = root.matches && root.matches(".tabs ul") ? [root] : root.querySelectorAll(".tabs ul");
    Array.prototype.forEach.call(rows, function (ul) {
      var current = ul.querySelector('[aria-current="page"]');
      if (current && ul.scrollWidth > ul.clientWidth) {
        ul.scrollLeft = Math.max(0, current.offsetLeft - (ul.clientWidth - current.offsetWidth) / 2);
      }
      if (!ul.hasAttribute("data-tabs")) {
        ul.setAttribute("data-tabs", "");
        ul.addEventListener("scroll", function () { tabMore(ul); }, { passive: true });
      }
      tabMore(ul);
    });
  }
  htmx.onLoad(tabRows);
  window.addEventListener("resize", function () { tabRows(document); });

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
    // A form that did not validate answers 422 with the page and its
    // errors (docs/03): show it, as a browser without JavaScript would.
    // htmx swaps no 4xx answer by default.
    if (e.detail.xhr && e.detail.xhr.status === 422) {
      e.detail.shouldSwap = true;
      e.detail.isError = false;
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

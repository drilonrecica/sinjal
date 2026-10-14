/* Beat bars (docs/04 "beat bar"): the tooltip and the slide-in. The server
 * renders every bar whole (web/templates/bars.templ), newest beat first;
 * this file only adds behaviour, so without it the bar and its label
 * still read.
 *
 * Tooltip: one element for the page, showing a beat's data-tip on hover;
 * on a focusable bar (the detail hero) also on focus, and the arrow keys,
 * Home and End move through the beats. A list row's beats come without
 * tips: the first hover fetches them from the bar's data-tips (a JSON
 * array, newest first, lined up with the beats).
 *
 * Slide-in: live.js has htmx swap a row or header for a fresh copy. Before
 * a swap each bar's data-newest is remembered by the id of the element
 * around it; when the copy is loaded with a newer one, the bar slides by a
 * beat and the newest grows in. Focus on a bar survives the swap. */
(function () {
  "use strict";

  var tip = null;
  var before = {}; // element id → {newest, focused, active} of its bar

  function tipElement() {
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "beat-tip";
      tip.setAttribute("role", "tooltip");
      tip.setAttribute("aria-live", "polite");
      tip.hidden = true;
      document.body.appendChild(tip);
    }
    return tip;
  }

  function show(beat) {
    var t = tipElement();
    var r = beat.getBoundingClientRect();
    t.textContent = beat.getAttribute("data-tip");
    t.hidden = false;
    var half = t.offsetWidth / 2 + 8;
    t.style.left = Math.min(Math.max(r.left + r.width / 2, half), window.innerWidth - half) + "px";
    t.style.top = r.top + "px";
  }

  function hide() {
    if (tip) {
      tip.hidden = true;
    }
  }

  function beats(bar) {
    return bar.querySelectorAll("i[data-tip]"); // newest first
  }

  function key(bar) {
    var owner = bar.parentElement && bar.parentElement.closest("[id]");
    return owner ? owner.id : null;
  }

  function activate(bar, beat) {
    var current = bar.querySelector("[data-active]");
    if (current) {
      current.removeAttribute("data-active");
    }
    if (beat) {
      beat.setAttribute("data-active", "");
      show(beat);
    }
  }

  // fetchTips gives a row's beats their tips once, then shows the one
  // still under the pointer.
  function fetchTips(bar) {
    var href = bar.getAttribute("data-tips");
    bar.removeAttribute("data-tips");
    fetch(href, {credentials: "same-origin"})
      .then(function (res) { return res.ok ? res.json() : []; })
      .then(function (tips) {
        var marks = bar.querySelectorAll("i[data-b]");
        tips.forEach(function (text, i) {
          if (marks[i]) {
            marks[i].setAttribute("data-tip", text);
          }
        });
        var hovered = bar.querySelector("i[data-tip]:hover");
        if (hovered) {
          show(hovered);
        }
      })
      .catch(function () {}); // no tooltip; the bar and its label still read
  }

  document.addEventListener("pointerover", function (e) {
    var bar = e.target.closest && e.target.closest("[data-beat-bar][data-tips]");
    if (bar) {
      fetchTips(bar);
    }
    var beat = e.target.closest && e.target.closest(".beat-bar > i[data-tip]");
    if (beat) {
      show(beat);
    } else if (!(document.activeElement && document.activeElement.matches("[data-beat-bar]"))) {
      hide();
    }
  });
  window.addEventListener("scroll", hide, true);

  document.addEventListener("focusin", function (e) {
    if (e.target.matches && e.target.matches("[data-beat-bar]")) {
      activate(e.target, beats(e.target)[0]);
    }
  });
  document.addEventListener("focusout", function (e) {
    if (e.target.matches && e.target.matches("[data-beat-bar]")) {
      activate(e.target, null);
      hide();
    }
  });

  document.addEventListener("keydown", function (e) {
    var bar = e.target;
    if (!bar.matches || !bar.matches("[data-beat-bar][tabindex]")) {
      return;
    }
    var list = Array.prototype.slice.call(beats(bar));
    if (!list.length) {
      return;
    }
    var i = list.indexOf(bar.querySelector("[data-active]"));
    // Older beats are to the left, later in the markup.
    var next = {ArrowLeft: i + 1, ArrowRight: i - 1, Home: list.length - 1, End: 0}[e.key];
    if (next === undefined) {
      return;
    }
    e.preventDefault();
    activate(bar, list[Math.min(Math.max(next, 0), list.length - 1)]);
  });

  document.addEventListener("htmx:beforeSwap", function (e) {
    var target = e.detail.target;
    var bars = target.querySelectorAll("[data-beat-bar]");
    Array.prototype.forEach.call(bars, function (bar) {
      var k = key(bar);
      var active = bar.querySelector("[data-active]");
      if (k) {
        before[k] = {
          newest: Number(bar.getAttribute("data-newest")),
          focused: document.activeElement === bar,
          active: active ? active.getAttribute("data-tip") : null
        };
      }
    });
  });

  document.addEventListener("htmx:load", function (e) {
    var root = e.detail.elt;
    var bars = root.matches("[data-beat-bar]") ? [root] : root.querySelectorAll("[data-beat-bar]");
    Array.prototype.forEach.call(bars, function (bar) {
      var k = key(bar);
      var was = k && before[k];
      if (!was) {
        return;
      }
      delete before[k];
      var first = bar.querySelector("i[data-b]");
      if (was.newest && first && Number(bar.getAttribute("data-newest")) > was.newest) {
        first.classList.add("beat-new");
        bar.setAttribute("data-slide", "");
      }
      if (was.focused) {
        var list = Array.prototype.slice.call(beats(bar));
        var tips = list.map(function (b) { return b.getAttribute("data-tip"); });
        var keep = tips.indexOf(was.active);
        bar.focus();
        activate(bar, list[keep >= 0 ? keep : 0]);
      }
    });
  });
})();

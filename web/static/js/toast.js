/* Feedback (docs/22 "Toasts" and "Live updates"). One toast region lives in
 * the shell, outside the pane, so it survives boosted navigation. It holds
 * two live regions that exist before anything is put in them: a polite one
 * (role="status") for confirmations and notes, an assertive one
 * (role="alert") for errors. Errors stay until dismissed; the rest leave
 * after a few seconds unless focus is inside them. A toast is never the
 * only place an error is explained: forms keep their own messages, and a
 * failed list fragment says so in place with a Retry.
 *
 * Raise one from anywhere:
 *   document.dispatchEvent(new CustomEvent("sinjal:toast", {detail: {message, kind}}))
 * or from the server with  HX-Trigger: {"toast": {"message": "...", "kind": "error"}}
 *
 * The event stream is announced only when it is lost for a while and when it
 * returns, never per event (rows update silently). */
(function () {
  "use strict";

  var region = document.querySelector("[data-toast-region]");
  if (!region) {
    return;
  }
  var polite = region.querySelector("[data-toast-polite]");
  var alerts = region.querySelector("[data-toast-alert]");
  var AUTO_MS = 6000;
  var STREAM_GRACE_MS = 8000;

  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)");

  // dismiss removes a toast; unless motion is reduced it fades out first.
  function dismiss(el) {
    if (!el || !el.parentNode || el.hasAttribute("data-leaving")) {
      return;
    }
    if (reduce.matches) {
      el.parentNode.removeChild(el);
      return;
    }
    el.setAttribute("data-leaving", "");
    setTimeout(function () {
      if (el.parentNode) {
        el.parentNode.removeChild(el);
      }
    }, 160);
  }

  // show puts a toast in the right live region. A toast with the same key
  // replaces the earlier one, so a repeating failure is one toast.
  function show(message, kind, key) {
    if (!message) {
      return null;
    }
    var error = kind === "error";
    var host = error ? alerts : polite;
    if (key) {
      Array.prototype.forEach.call(region.querySelectorAll("[data-toast-key]"), function (t) {
        if (t.getAttribute("data-toast-key") === key) {
          dismiss(t);
        }
      });
    }
    var el = document.createElement("div");
    el.className = "toast";
    el.setAttribute("data-kind", error ? "error" : "info");
    if (key) {
      el.setAttribute("data-toast-key", key);
    }
    var body = document.createElement("p");
    body.className = "toast-body";
    body.textContent = message;
    var close = document.createElement("button");
    close.type = "button";
    close.className = "button button-quiet";
    close.textContent = "Dismiss";
    close.addEventListener("click", function () {
      dismiss(el);
    });
    el.appendChild(body);
    el.appendChild(close);
    host.appendChild(el);
    if (!error) {
      setTimeout(function wait() {
        if (el.contains(document.activeElement)) {
          setTimeout(wait, AUTO_MS);
        } else {
          dismiss(el);
        }
      }, AUTO_MS);
    }
    return el;
  }

  // Escape closes the toast the focus is in.
  region.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      dismiss(e.target.closest && e.target.closest(".toast"));
    }
  });

  document.addEventListener("sinjal:toast", function (e) {
    var d = e.detail || {};
    show(d.message, d.kind);
  });
  // HX-Trigger: {"toast": {...}} arrives as a "toast" event; a bare string
  // arrives as detail.value.
  document.addEventListener("toast", function (e) {
    var d = e.detail || {};
    show(d.message || d.value, d.kind);
  });

  // --- Requests that fail ---
  function errorText(xhr) {
    var s = xhr ? xhr.status : 0;
    if (s === 403) {
      return "You are not allowed to do that.";
    }
    if (s === 429) {
      return "Too many requests. Wait a moment and try again.";
    }
    if (s === 413) {
      return "That was too large to send.";
    }
    if (s >= 500) {
      return "Sinjal could not finish that (server error). Try again.";
    }
    return "That did not go through. Reload the page and try again.";
  }

  // The list column's fragments are replaced by an in-place note; they
  // are fetched on their own, so a failure must not leave "Loading…".
  function inPlace(target) {
    return target && target.matches && target.matches(".column-body, .column-panel, [data-activity]");
  }

  function retryNote(target, path) {
    target.textContent = "";
    var p = document.createElement("p");
    p.className = "column-note";
    p.setAttribute("role", "status");
    p.appendChild(document.createTextNode("This could not be loaded. "));
    var b = document.createElement("button");
    b.type = "button";
    b.className = "button button-quiet";
    b.textContent = "Retry";
    b.addEventListener("click", function () {
      htmx.ajax("GET", path, { target: target, swap: "innerHTML" });
    });
    p.appendChild(b);
    target.appendChild(p);
  }

  function failed(e, text) {
    var cfg = e.detail.requestConfig || {};
    var target = e.detail.target;
    if (cfg.verb === "get" && !cfg.boosted) {
      if (inPlace(target)) {
        retryNote(target, cfg.path);
      }
      // Other GET fragments are live refreshes: they keep what they show
      // and the next event or reconnect refreshes them again.
      return;
    }
    show(text, "error", "request-failed");
  }

  document.addEventListener("htmx:responseError", function (e) {
    failed(e, errorText(e.detail.xhr));
  });
  document.addEventListener("htmx:sendError", function (e) {
    failed(e, "Sinjal could not be reached. Check the connection and try again.");
  });
  document.addEventListener("htmx:timeout", function (e) {
    failed(e, "Sinjal took too long to answer. Try again.");
  });

  // --- Loading ---
  document.addEventListener("htmx:beforeRequest", function (e) {
    var t = e.detail.target;
    if (t && t.setAttribute) {
      t.setAttribute("aria-busy", "true");
    }
  });
  document.addEventListener("htmx:afterRequest", function (e) {
    var t = e.detail.target;
    if (t && t.removeAttribute) {
      t.removeAttribute("aria-busy");
    }
  });

  // --- The event stream ---
  var lost = null;
  var lostToast = null;
  function streamLost() {
    if (lost === null && lostToast === null) {
      lost = setTimeout(function () {
        lost = null;
        lostToast = show("Live updates are paused. Reconnecting…", "info", "stream");
      }, STREAM_GRACE_MS);
    }
  }
  document.addEventListener("htmx:sseError", streamLost);
  document.addEventListener("htmx:sseClose", streamLost);
  document.addEventListener("htmx:sseOpen", function () {
    if (lost !== null) {
      clearTimeout(lost);
      lost = null;
    }
    if (lostToast !== null) {
      dismiss(lostToast);
      lostToast = null;
      show("Live updates resumed.", "info", "stream");
    }
  });
})();

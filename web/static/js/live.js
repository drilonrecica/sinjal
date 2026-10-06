/* Live updates (docs/32_SSE_EVENTS.md). The htmx SSE extension owns the
 * EventSource and re-dispatches each server event as a DOM event on the
 * <div sse-connect>. The payload is only {"monitor_id": "..."}: this file
 * finds the elements showing that monitor and asks htmx to refetch their
 * fragment. It never renders anything itself.
 *
 * [data-live][data-monitor-id]  one monitor (row, detail header)
 * [data-live-list]              a whole list, refetched when monitors are
 *                               created or deleted, or maintenance changes
 * [data-gone-href]              where to go when its monitor is deleted */
(function () {
  "use strict";

  function monitorID(e) {
    try {
      return JSON.parse(e.detail.data).monitor_id;
    } catch (err) {
      return null; // not ours; the next event or reconnect refreshes anyway
    }
  }

  function each(root, selector, fn) {
    Array.prototype.forEach.call(root.querySelectorAll(selector), fn);
  }

  function refresh(el) {
    htmx.trigger(el, "refresh");
  }

  function showing(id, fn) {
    each(document, "[data-live]", function (el) {
      if (id && el.getAttribute("data-monitor-id") === id) {
        fn(el);
      }
    });
  }

  document.addEventListener("sse:monitor.updated", function (e) {
    showing(monitorID(e), refresh);
  });

  // A list refetches itself whole: monitors created, maintenance changed.
  // A page only receives the events its <div sse-connect> names.
  document.addEventListener("sse:monitor.created", function () {
    each(document, "[data-live-list]", refresh);
  });

  document.addEventListener("sse:maintenance.updated", function () {
    each(document, "[data-live-list]", refresh);
  });

  document.addEventListener("sse:monitor.deleted", function (e) {
    showing(monitorID(e), function (el) {
      var href = el.getAttribute("data-gone-href");
      if (href) {
        window.location.assign(href);
      }
    });
    each(document, "[data-live-list]", refresh);
  });

  // No event is replayed after a gap, so whenever a stream opens (first load,
  // reconnect, server restart) everything it feeds is refreshed once. A list
  // brings its rows along.
  document.addEventListener("htmx:sseOpen", function (e) {
    each(e.target, "[data-live-list]", refresh);
    each(e.target, "[data-live]", function (el) {
      if (!el.closest("[data-live-list]")) {
        refresh(el);
      }
    });
  });
})();

/* Live updates (docs/32_SSE_EVENTS.md). The htmx SSE extension owns the
 * EventSource and re-dispatches each server event as a DOM event on the
 * <div sse-connect>. The payload is only {"monitor_id": "..."}: this file
 * finds the elements showing that monitor and asks htmx to refetch their
 * fragment. It never renders anything itself. */
(function () {
  "use strict";

  function live(root) {
    return root.querySelectorAll("[data-live]");
  }

  document.addEventListener("sse:monitor.updated", function (e) {
    var id;
    try {
      id = JSON.parse(e.detail.data).monitor_id;
    } catch (err) {
      return; // not ours; the next event or reconnect refreshes anyway
    }
    live(document).forEach(function (el) {
      if (el.getAttribute("data-monitor-id") === id) {
        htmx.trigger(el, "refresh");
      }
    });
  });

  // No event is replayed after a gap, so whenever a stream opens (first load,
  // reconnect, server restart) everything it feeds is refreshed once.
  document.addEventListener("htmx:sseOpen", function (e) {
    live(e.target).forEach(function (el) {
      htmx.trigger(el, "refresh");
    });
  });
})();

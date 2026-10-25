/* The monitor form (docs/03 "Monitor creation/editing"). The form works
 * without this file; it adds two conveniences:
 * - the name follows the address ([data-name-source] → [data-name-target])
 *   until it is typed or the form already had one: "https://api.example.com/health"
 *   names the monitor "api.example.com";
 * - Test now (#test-now, rendered hidden) is shown: it posts with htmx. */
(function () {
  "use strict";

  // nameFrom is the host of a URL or the host as typed, "" if none.
  function nameFrom(value) {
    value = value.trim();
    if (!value) {
      return "";
    }
    try {
      return new URL(value).hostname;
    } catch (err) {
      return value.replace(/^[a-z]+:\/\//i, "").split(/[\/:?#]/)[0];
    }
  }

  function scan(root) {
    Array.prototype.forEach.call(root.querySelectorAll("form.monitor-form"), function (form) {
      if (form.sinjalMonitorForm) {
        return;
      }
      form.sinjalMonitorForm = true;
      var test = form.querySelector("#test-now");
      if (test && window.htmx) {
        test.hidden = false;
      }
      var source = form.querySelector("[data-name-source]");
      var target = form.querySelector("[data-name-target]");
      if (!source || !target) {
        return;
      }
      var follow = target.value === "" || target.value === nameFrom(source.value);
      target.addEventListener("input", function () {
        follow = target.value === "";
      });
      source.addEventListener("input", function () {
        if (follow) {
          target.value = nameFrom(source.value);
        }
      });
    });
  }

  // The app shell swaps pages in with htmx; a new form gets bound too.
  scan(document);
  if (window.htmx) {
    htmx.onLoad(scan);
  }
})();

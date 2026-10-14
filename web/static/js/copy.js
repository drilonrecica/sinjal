/* Copy buttons: <button data-copy="ID" hidden> copies the text of the
 * element with that id. Without JavaScript or the Clipboard API the
 * buttons stay hidden and the text can be selected by hand. */
(function () {
  "use strict";
  if (!navigator.clipboard) {
    return;
  }
  function scan(root) {
    Array.prototype.forEach.call(root.querySelectorAll("button[data-copy]"), function (btn) {
      if (btn.sinjalCopy) {
        return;
      }
      btn.sinjalCopy = true;
      var label = btn.textContent;
      btn.hidden = false;
      btn.addEventListener("click", function () {
        var src = document.getElementById(btn.getAttribute("data-copy"));
        navigator.clipboard.writeText(src.textContent).then(
          function () { btn.textContent = "Copied"; },
          function () { btn.textContent = "Copy failed: select the text"; }
        );
        setTimeout(function () { btn.textContent = label; }, 3000);
      });
    });
  }

  // Content swapped in by the app shell (htmx) gets its buttons too.
  scan(document);
  if (window.htmx) {
    htmx.onLoad(scan);
  }
})();

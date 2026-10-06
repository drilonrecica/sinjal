/* Latency chart (docs/04 "Charts", docs/33). The server puts the series
 * and the overlays as JSON in data attributes of [data-chart]; this file
 * draws them with uPlot. Colours come from the theme's tokens and are read
 * again when the theme changes. Nothing here renders text from the data:
 * uPlot's legend shows numbers formatted below. The same figures are in
 * the text summary the chart points to.
 *
 * data-series    {"t":[unix s], "avg":[ms|null], "max":[ms|null], "fail":[n]}
 * data-overlays  {"from", "to", "down":[[a,b]], "maint":[[a,b]], "paused":[[a,b]], "marks":[t]}
 * data-tz        the instance time zone for the time axis */
(function () {
  "use strict";

  function token(el, name) {
    return getComputedStyle(el).getPropertyValue(name).trim();
  }

  function ms(v) {
    if (v == null) {
      return "—";
    }
    return v < 1000 ? Math.round(v) + " ms" : (v / 1000).toFixed(1) + " s";
  }

  // bands fills the overlay spans behind the series.
  function bands(u, spans, colour, alpha) {
    var ctx = u.ctx, top = u.bbox.top, height = u.bbox.height;
    var left = u.bbox.left, right = u.bbox.left + u.bbox.width;
    ctx.save();
    ctx.globalAlpha = alpha;
    ctx.fillStyle = colour;
    spans.forEach(function (s) {
      var x0 = Math.max(left, u.valToPos(s[0], "x", true));
      var x1 = Math.min(right, u.valToPos(s[1], "x", true));
      if (x1 > x0) {
        ctx.fillRect(x0, top, Math.max(x1 - x0, 1), height);
      }
    });
    ctx.restore();
  }

  // clock formats unix seconds in the instance time zone, 24-hour: the
  // time alone over a short range, the day as well over a longer one.
  function clock(tz, withDay) {
    var opts = { timeZone: tz, hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
    if (withDay) {
      opts.day = "numeric";
      opts.month = "short";
    }
    var f = new Intl.DateTimeFormat(undefined, opts);
    return function (s) { return f.format(new Date(s * 1e3)); };
  }

  function build(el) {
    var series = JSON.parse(el.getAttribute("data-series"));
    var over = JSON.parse(el.getAttribute("data-overlays"));
    var tz = el.getAttribute("data-tz") || "UTC";
    var c = {
      line: token(el, "--chart-line"),
      grid: token(el, "--chart-grid"),
      outage: token(el, "--chart-outage"),
      maint: token(el, "--chart-maintenance"),
      paused: token(el, "--status-paused"),
      text: token(el, "--text-muted")
    };
    var dpr = window.devicePixelRatio || 1;
    var long = over.to - over.from > 2 * 86400;
    var tick = clock(tz, long), full = clock(tz, true);
    var axis = { stroke: c.text, grid: { stroke: c.grid, width: 1 }, ticks: { stroke: c.grid, width: 1 } };
    var opts = {
      width: el.clientWidth,
      height: el.clientWidth < 480 ? 200 : 240,
      tzDate: function (ts) { return uPlot.tzDate(new Date(ts * 1e3), tz); },
      cursor: { drag: { x: false, y: false }, points: { size: 6 } },
      legend: { live: true },
      scales: {
        x: { time: true, range: [over.from, over.to] },
        y: { range: function (u, min, max) { return [0, max > 0 ? max * 1.1 : 1]; } },
        n: { range: [0, 1] }
      },
      series: [
        { label: "Time", value: function (u, v) { return v == null ? "—" : full(v); } },
        { label: "Average", stroke: c.line, width: 1.5, value: function (u, v) { return ms(v); } },
        { label: "Maximum", stroke: c.line, width: 1, dash: [4, 4], value: function (u, v) { return ms(v); } },
        { label: "Failed", scale: "n", stroke: c.outage, width: 0, paths: function () { return null; },
          points: { show: false }, value: function (u, v) { return v == null ? "—" : String(v); } }
      ],
      axes: [
        Object.assign({}, axis, { values: function (u, vals) { return vals.map(tick); } }),
        Object.assign({}, axis, { size: 64, values: function (u, vals) { return vals.map(ms); } })
      ],
      hooks: {
        drawClear: [function (u) {
          bands(u, over.paused, c.paused, 0.22);
          bands(u, over.maint, c.maint, 0.22);
          bands(u, over.down, c.outage, 0.18);
        }],
        draw: [function (u) {
          var ctx = u.ctx;
          ctx.save();
          ctx.strokeStyle = c.outage;
          ctx.lineWidth = 2 * dpr;
          over.marks.forEach(function (t) {
            var x = Math.round(u.valToPos(t, "x", true));
            ctx.beginPath();
            ctx.moveTo(x, u.bbox.top);
            ctx.lineTo(x, u.bbox.top + 8 * dpr);
            ctx.stroke();
          });
          ctx.restore();
        }]
      }
    };
    el.textContent = "";
    return new uPlot(opts, [series.t, series.avg, series.max, series.fail], el);
  }

  document.querySelectorAll("[data-chart]").forEach(function (el) {
    var plot = build(el);
    if (window.ResizeObserver) {
      new ResizeObserver(function () {
        plot.setSize({ width: el.clientWidth, height: el.clientWidth < 480 ? 200 : 240 });
      }).observe(el);
    }
    // A theme change swaps the tokens: draw again in the new colours.
    new MutationObserver(function () {
      plot.destroy();
      plot = build(el);
    }).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  });
})();

// Small canvas charts for the run-comparison panel. No dependencies.
//
// DnsCharts.bar -- a bar chart:
//
//   const chart = DnsCharts.bar(canvas, {
//     labels: [['2026-10-05 08:02:39', '127.0.0.1/UDP'], ...],  // one entry per bar; an array is several lines
//     values: [312000, 301500, null],                            // null = no bar
//     theme:  { text, grid, bg },                                // CSS colours
//     fill, stroke, radius, width,                               // bar look; width is the share of its slot (0-1)
//     yTitle, yColor, yFormat(v), ySuggestedMax,                 // value axis (always starts at 0)
//     xFontPx,                                                   // category label size
//     refLines: [{ value, color, label }],                       // dashed horizontal guides
//     marks:    [{ index, text, color }],                        // labels pinned to the top over a bar
//     tip(i):   { title: [...], body: [...] },                   // hover text for bar i
//   });
//   chart.destroy();
//
// DnsCharts.pie -- a pie chart; one slice per entry, clockwise from 12 o'clock:
//
//   const chart = DnsCharts.pie(canvas, {
//     values: [48210, 35120, null],      // null or <= 0 = no slice
//     colors: ['hsl(...)', ...],          // one CSS colour per entry
//     theme:  { text, grid, bg },         // bg also separates the slices
//     tip(i): { title: [...], body: [...] },
//     emptyText: 'No data',               // shown when nothing can be drawn
//     winners: [0],                       // indices to feature: pushed out, enlarged, outlined and glowing
//   });
//   chart.destroy();
//
// Both fill its parent (which needs a definite size) and redraws when that
// size changes, including when a hidden parent is first shown.
(function () {
  'use strict';

  // Axis ticks at 1, 2, 2.5, 5 x 10^n; the axis ends on the first tick at or above max.
  function niceTicks(max, maxTicks) {
    if (!(max > 0)) return { ticks: [0, 1], max: 1 };
    var rough = max / Math.max(1, maxTicks - 1);
    var pow = Math.pow(10, Math.floor(Math.log10(rough)));
    var f = rough / pow;
    var step = (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * pow;
    var top = Math.ceil(max / step - 1e-9) * step, ticks = [];
    for (var v = 0; v <= top + step * 1e-6; v += step) ticks.push(+v.toFixed(10));
    return { ticks: ticks, max: top };
  }

  function roundTopRect(ctx, x, y, w, h, r) {
    r = Math.max(0, Math.min(r, w / 2, h));
    ctx.beginPath();
    ctx.moveTo(x, y + h);
    ctx.lineTo(x, y + r);
    ctx.arcTo(x, y, x + r, y, r);
    ctx.lineTo(x + w - r, y);
    ctx.arcTo(x + w, y, x + w, y + r, r);
    ctx.lineTo(x + w, y + h);
    ctx.closePath();
  }

  function bar(canvas, spec) {
    var host = canvas.parentElement;
    var ctx = canvas.getContext('2d');
    var family = getComputedStyle(canvas).fontFamily || 'sans-serif';
    var th = spec.theme || {};
    var labels = spec.labels.map(function (l) { return Array.isArray(l) ? l : String(l).split(','); });
    var values = spec.values;
    var geom = null; // bar rectangles from the last draw, for hover
    var tipEl = null;
    var dead = false;

    canvas.style.cssText = 'display:block;width:100%;height:100%';
    if (getComputedStyle(host).position === 'static') host.style.position = 'relative';

    function font(px, bold) { return (bold ? 'bold ' : '') + px + 'px ' + family; }

    function draw() {
      if (dead) return;
      var w = host.clientWidth, h = host.clientHeight;
      if (w < 20 || h < 20) { geom = null; return; }
      var dpr = window.devicePixelRatio || 1;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

      ctx.fillStyle = th.bg || 'transparent';
      ctx.fillRect(0, 0, w, h);

      var n = values.length;
      var yColor = spec.yColor || th.text;
      var yFmt = spec.yFormat || String;

      // Category labels: shrink the text until the widest line fits its slot.
      var xPx = spec.xFontPx || 14;
      var left0 = 40;
      var slot0 = Math.max(1, (w - left0 - 4) / Math.max(1, n));
      ctx.font = font(xPx);
      for (; xPx > 9; xPx--) {
        ctx.font = font(xPx);
        var widest = 0;
        labels.forEach(function (l) { l.forEach(function (s) { widest = Math.max(widest, ctx.measureText(s).width); }); });
        if (widest <= slot0 - 8) break;
      }
      var lineH = Math.round(xPx * 1.25);
      var maxLines = labels.reduce(function (m, l) { return Math.max(m, l.length); }, 1);
      var xAxisH = 8 + maxLines * lineH + 4;

      var top = 8, bottom = h - xAxisH;
      var plotH = Math.max(10, bottom - top);

      var dataMax = Math.max(spec.ySuggestedMax || 0, Math.max.apply(null, values.map(function (v) { return v == null ? 0 : v; })));
      var axis = niceTicks(dataMax, Math.floor(plotH / 22) + 1);

      ctx.font = font(11);
      var tickW = 0;
      axis.ticks.forEach(function (t) { tickW = Math.max(tickW, ctx.measureText(yFmt(t)).width); });
      var titleW = spec.yTitle ? 13 + 6 : 0;
      var left = Math.round(6 + titleW + tickW + 8), right = w - 4;
      var plotW = Math.max(10, right - left);
      var slot = plotW / Math.max(1, n);
      var yOf = function (v) { return bottom - (v / axis.max) * plotH; };

      // grid
      ctx.lineWidth = 1;
      ctx.strokeStyle = th.grid;
      ctx.beginPath();
      axis.ticks.forEach(function (t) { var y = Math.round(yOf(t)) + .5; ctx.moveTo(left, y); ctx.lineTo(right, y); });
      for (var i = 0; i <= n; i++) { var x = Math.round(left + i * slot) + .5; ctx.moveTo(x, top); ctx.lineTo(x, bottom + 4); }
      ctx.stroke();

      // bars
      var bw = slot * (spec.width || .72);
      geom = [];
      for (var k = 0; k < n; k++) {
        var cx = left + (k + .5) * slot, v = values[k];
        if (v == null) { geom.push(null); continue; }
        var y = yOf(v), x0 = cx - bw / 2;
        geom.push({ x: x0, y: y, w: bw, h: bottom - y, cx: cx });
        roundTopRect(ctx, x0, y, bw, bottom - y, spec.radius || 0);
        ctx.fillStyle = spec.fill;
        ctx.fill();
        if (spec.stroke) { ctx.lineWidth = 1; ctx.strokeStyle = spec.stroke; ctx.stroke(); }
      }

      // reference lines
      (spec.refLines || []).forEach(function (r) {
        var yp = yOf(r.value);
        if (yp < top || yp > bottom) return;
        ctx.save();
        ctx.setLineDash([5, 4]);
        ctx.strokeStyle = r.color; ctx.lineWidth = 1.5; ctx.globalAlpha = .8;
        ctx.beginPath(); ctx.moveTo(left, yp); ctx.lineTo(right, yp); ctx.stroke();
        ctx.setLineDash([]); ctx.globalAlpha = 1;
        ctx.fillStyle = r.color; ctx.font = font(11, true); ctx.textAlign = 'right';
        ctx.fillText(r.label, right - 4, yp - 4);
        ctx.restore();
      });

      // y tick labels and title
      ctx.fillStyle = yColor; ctx.font = font(11); ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
      axis.ticks.forEach(function (t) { ctx.fillText(yFmt(t), left - 8, yOf(t)); });
      if (spec.yTitle) {
        ctx.save();
        ctx.translate(6 + 6, top + plotH / 2); ctx.rotate(-Math.PI / 2);
        ctx.font = font(11, true); ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
        ctx.fillText(spec.yTitle, 0, 0);
        ctx.restore();
      }

      // axis lines
      ctx.strokeStyle = th.grid; ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(left + .5, top); ctx.lineTo(left + .5, bottom + 4);
      ctx.moveTo(left, Math.round(bottom) + .5); ctx.lineTo(right, Math.round(bottom) + .5);
      ctx.stroke();

      // category labels
      ctx.fillStyle = th.text; ctx.font = font(xPx); ctx.textAlign = 'center'; ctx.textBaseline = 'alphabetic';
      labels.forEach(function (l, i) {
        l.forEach(function (s, j) { ctx.fillText(s, left + (i + .5) * slot, bottom + 8 + xPx + j * lineH - 2); });
      });

      // pinned marks (error counts), over a background patch so they stay legible
      (spec.marks || []).forEach(function (m) {
        var g = geom[m.index];
        if (!g) return;
        ctx.save();
        ctx.font = font(14, true); ctx.textAlign = 'center'; ctx.textBaseline = 'alphabetic';
        var tw = ctx.measureText(m.text).width, by = top + 16;
        ctx.fillStyle = th.bg || '#000';
        ctx.fillRect(g.cx - tw / 2 - 5, by - 13, tw + 10, 18);
        ctx.fillStyle = m.color;
        ctx.fillText(m.text, g.cx, by);
        ctx.restore();
      });
    }

    // hover tooltip
    function hideTip() { if (tipEl) tipEl.style.display = 'none'; }
    function onMove(e) {
      if (!geom || !spec.tip) return hideTip();
      var r = canvas.getBoundingClientRect(), px = e.clientX - r.left, py = e.clientY - r.top;
      var hit = -1;
      for (var i = 0; i < geom.length; i++) {
        var g = geom[i];
        if (g && px >= g.x && px <= g.x + g.w && py >= g.y && py <= g.y + g.h) { hit = i; break; }
      }
      if (hit < 0) return hideTip();
      var t = spec.tip(hit);
      if (!tipEl) {
        tipEl = document.createElement('div');
        tipEl.setAttribute('role', 'tooltip');
        tipEl.style.cssText = 'position:absolute;z-index:5;pointer-events:none;background:rgba(0,0,0,.85);color:#fff;' +
          'font:12px/1.4 ' + family + ';padding:6px 9px;border-radius:6px;white-space:pre;display:none';
        host.appendChild(tipEl);
      }
      tipEl.textContent = '';
      (t.title || []).forEach(function (s) {
        var d = document.createElement('div'); d.style.fontWeight = 'bold'; d.textContent = s; tipEl.appendChild(d);
      });
      (t.body || []).forEach(function (s) {
        if (!s) return;
        var d = document.createElement('div'); d.textContent = s; tipEl.appendChild(d);
      });
      tipEl.style.display = 'block';
      var tw = tipEl.offsetWidth, thh = tipEl.offsetHeight, g = geom[hit];
      var x = Math.min(Math.max(2, g.cx - tw / 2), host.clientWidth - tw - 2);
      var y = g.y - thh - 8;
      if (y < 2) y = Math.min(g.y + 8, host.clientHeight - thh - 2);
      tipEl.style.left = x + 'px';
      tipEl.style.top = y + 'px';
    }

    var ro = typeof ResizeObserver === 'function' ? new ResizeObserver(draw) : null;
    if (ro) ro.observe(host); else window.addEventListener('resize', draw);
    canvas.addEventListener('pointermove', onMove);
    canvas.addEventListener('pointerleave', hideTip);
    draw();

    return {
      destroy: function () {
        dead = true;
        if (ro) ro.disconnect(); else window.removeEventListener('resize', draw);
        canvas.removeEventListener('pointermove', onMove);
        canvas.removeEventListener('pointerleave', hideTip);
        if (tipEl) { tipEl.remove(); tipEl = null; }
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.clearRect(0, 0, canvas.width, canvas.height);
      },
    };
  }

  // ---- pie ----------------------------------------------------------------
  function pie(canvas, spec) {
    var host = canvas.parentElement;
    var ctx = canvas.getContext('2d');
    var family = getComputedStyle(canvas).fontFamily || 'sans-serif';
    var th = spec.theme || {};
    var values = spec.values;
    var total = 0;
    values.forEach(function (v) { if (v > 0) total += v; });
    var slices = null;   // {i, a0, a1, share} from the last draw
    var cx = 0, cy = 0, rad = 0;
    var hover = -1;
    var isWin = function (i) { return (spec.winners || []).indexOf(i) >= 0; };
    var WIN_PUSH = 10, WIN_GROW = 8;   // how far a featured slice moves out, and how much larger it is
    var tipEl = null;
    var dead = false;

    canvas.style.cssText = 'display:block;width:100%;height:100%';
    if (getComputedStyle(host).position === 'static') host.style.position = 'relative';

    function draw() {
      if (dead) return;
      var w = host.clientWidth, h = host.clientHeight;
      if (w < 20 || h < 20) { slices = null; return; }
      var dpr = window.devicePixelRatio || 1;
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.fillStyle = th.bg || 'transparent';
      ctx.fillRect(0, 0, w, h);

      slices = [];
      if (!(total > 0)) {
        ctx.fillStyle = th.text; ctx.font = '13px ' + family; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
        ctx.fillText(spec.emptyText || 'No data', w / 2, h / 2);
        return;
      }
      cx = w / 2; cy = h / 2;
      var drawn = values.filter(function (v) { return v > 0; }).length;   // slices.length is still 0 here
      var anyWin = drawn > 1 && values.some(function (v, i) { return v > 0 && isWin(i); });
      rad = Math.max(10, Math.min(w, h) / 2 - 10 - (anyWin ? WIN_PUSH + WIN_GROW + 4 : 0));

      var a = -Math.PI / 2;
      values.forEach(function (v, i) {
        if (!(v > 0)) return;
        var share = v / total, a1 = a + share * Math.PI * 2;
        slices.push({ i: i, a0: a, a1: a1, share: share });
        a = a1;
      });
      // where each slice sits: centre and radius (a featured slice is pushed out and larger)
      slices.forEach(function (sl) {
        var mid = (sl.a0 + sl.a1) / 2, win = anyWin && isWin(sl.i);
        var push = (win ? WIN_PUSH : 0) + (sl.i === hover ? 4 : 0);
        sl.win = win;
        sl.x = cx + Math.cos(mid) * push; sl.y = cy + Math.sin(mid) * push;
        sl.r = rad + (win ? WIN_GROW : 0);
        sl.mid = mid;
      });

      // others first, featured slices last so their outline and glow sit on top
      slices.slice().sort(function (p, q) { return (p.win ? 1 : 0) - (q.win ? 1 : 0); }).forEach(function (sl) {
        ctx.save();
        ctx.beginPath();
        if (slices.length > 1) ctx.moveTo(sl.x, sl.y);
        ctx.arc(sl.x, sl.y, sl.r, sl.a0, sl.a1);
        ctx.closePath();
        if (sl.win) { ctx.shadowColor = spec.colors[sl.i]; ctx.shadowBlur = 22; }
        else if (anyWin) ctx.globalAlpha = 0.72;
        ctx.fillStyle = spec.colors[sl.i];
        ctx.fill();
        ctx.shadowBlur = 0; ctx.globalAlpha = 1;
        if (sl.win) { ctx.lineWidth = 3; ctx.strokeStyle = th.text || '#fff'; ctx.stroke(); }
        else if (slices.length > 1) { ctx.lineWidth = 2; ctx.strokeStyle = th.bg || '#000'; ctx.stroke(); }
        ctx.restore();
      });

      // percentage inside each slice big enough to hold it; featured slices also say "Best"
      ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      slices.forEach(function (sl) {
        if (sl.share < 0.06) return;
        var tx = sl.x + Math.cos(sl.mid) * sl.r * 0.62, ty = sl.y + Math.sin(sl.mid) * sl.r * 0.62;
        var txt = (sl.share * 100).toFixed(sl.share >= 0.995 ? 0 : 1) + '%';
        var big = sl.win && sl.share >= 0.12;
        ctx.font = 'bold ' + (sl.win ? 15 : 13) + 'px ' + family;
        var dy = big ? -8 : 0;
        ctx.fillStyle = 'rgba(0,0,0,.35)'; ctx.fillText(txt, tx + 1, ty + dy + 1);
        ctx.fillStyle = '#fff'; ctx.fillText(txt, tx, ty + dy);
        if (big) {
          ctx.font = 'bold 11px ' + family;
          ctx.fillStyle = 'rgba(0,0,0,.35)'; ctx.fillText('\u2605 BEST', tx + 1, ty + 9);
          ctx.fillStyle = '#fff'; ctx.fillText('\u2605 BEST', tx, ty + 8);
        }
      });
    }

    function hit(e) {
      if (!slices || !slices.length) return -1;
      var r = canvas.getBoundingClientRect(), px = e.clientX - r.left, py = e.clientY - r.top;
      // test the featured (top-most) slices first, each against its own centre and radius
      var order = slices.slice().sort(function (p, q) { return (q.win ? 1 : 0) - (p.win ? 1 : 0); });
      for (var k = 0; k < order.length; k++) {
        var sl = order[k], dx = px - sl.x, dy = py - sl.y;
        if (Math.sqrt(dx * dx + dy * dy) > sl.r) continue;
        if (slices.length === 1) return sl.i;
        var ang = Math.atan2(dy, dx);
        if (ang < -Math.PI / 2) ang += Math.PI * 2;      // slices start at 12 o'clock
        if (ang >= sl.a0 && ang <= sl.a1) return sl.i;
      }
      return -1;
    }

    function hideTip() {
      if (tipEl) tipEl.style.display = 'none';
      if (hover !== -1) { hover = -1; draw(); }
    }
    function onMove(e) {
      var i = hit(e);
      if (i < 0) return hideTip();
      if (i !== hover) { hover = i; draw(); }
      if (!spec.tip) return;
      var t = spec.tip(i);
      if (!tipEl) {
        tipEl = document.createElement('div');
        tipEl.setAttribute('role', 'tooltip');
        tipEl.style.cssText = 'position:absolute;z-index:5;pointer-events:none;background:rgba(0,0,0,.85);color:#fff;' +
          'font:12px/1.4 ' + family + ';padding:6px 9px;border-radius:6px;white-space:pre;display:none';
        host.appendChild(tipEl);
      }
      tipEl.textContent = '';
      (t.title || []).forEach(function (s) {
        var d = document.createElement('div'); d.style.fontWeight = 'bold'; d.textContent = s; tipEl.appendChild(d);
      });
      (t.body || []).forEach(function (s) {
        if (!s) return;
        var d = document.createElement('div'); d.textContent = s; tipEl.appendChild(d);
      });
      tipEl.style.display = 'block';
      var r = canvas.getBoundingClientRect();
      var tw = tipEl.offsetWidth, thh = tipEl.offsetHeight;
      var x = Math.min(Math.max(2, e.clientX - r.left + 12), host.clientWidth - tw - 2);
      var y = Math.min(Math.max(2, e.clientY - r.top + 12), host.clientHeight - thh - 2);
      tipEl.style.left = x + 'px';
      tipEl.style.top = y + 'px';
    }

    var ro = typeof ResizeObserver === 'function' ? new ResizeObserver(draw) : null;
    if (ro) ro.observe(host); else window.addEventListener('resize', draw);
    canvas.addEventListener('pointermove', onMove);
    canvas.addEventListener('pointerleave', hideTip);
    draw();

    return {
      destroy: function () {
        dead = true;
        if (ro) ro.disconnect(); else window.removeEventListener('resize', draw);
        canvas.removeEventListener('pointermove', onMove);
        canvas.removeEventListener('pointerleave', hideTip);
        if (tipEl) { tipEl.remove(); tipEl = null; }
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.clearRect(0, 0, canvas.width, canvas.height);
      },
    };
  }

  window.DnsCharts = { bar: bar, pie: pie };
})();

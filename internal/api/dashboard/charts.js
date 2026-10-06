// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Draws the dashboard charts. A page embeds each chart's data as JSON in a
// <script type="application/json"> element and marks the container with
// data-chart (the form) and data-src (that element's id). Every value a chart
// shows is also on the page as a label or table cell. The charts add shape
// and a hover readout.
//
// A form draws into the frame it asks for and returns it with two hover
// hooks: cur, the marks that follow the pointer, and at(px, py), which moves
// them to a plot position and returns the tip's title and rows.
(() => {
  const NS = 'http://www.w3.org/2000/svg';
  const HOUR = 3600, DAY = 86400;
  const num = new Intl.NumberFormat('en-US');
  const compact = new Intl.NumberFormat('en-US', {notation: 'compact', maximumFractionDigits: 1});
  const dateFmt = new Intl.DateTimeFormat('en-US', {month: 'short', day: 'numeric', timeZone: 'UTC'});
  const day = t => dateFmt.format(t * 1000);
  const clock = t => new Date(t * 1000).toISOString().slice(11, 16);
  const hour = t => `${day(t)}, ${clock(t)} UTC`;
  const iso = t => new Date(t * 1000).toISOString().slice(0, 10);
  const formats = {
    num: v => num.format(v),
    compact: v => compact.format(v),
    hours: v => `${v.toFixed(v < 10 ? 1 : 0)} h`,
  };
  const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
  // bin picks which of n bins of width w holds px.
  const bin = (px, w, n) => Math.min(n - 1, Math.floor(px / w));

  function attr(e, attrs) {
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    return e;
  }
  function svg(tag, attrs, parent) {
    return parent.appendChild(attr(document.createElementNS(NS, tag), attrs));
  }
  function text(parent, attrs, s) {
    svg('text', attrs, parent).textContent = s;
  }
  function html(tag, cls, s) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    e.textContent = s;
    return e;
  }

  // One tooltip serves every chart and segmented bar. Rows lead with the
  // value and key the series with a short stroke.
  const tip = html('div', 'viz-tip', '');
  tip.hidden = true;
  document.body.append(tip);
  function showTip(ev, title, rows) {
    tip.replaceChildren();
    if (title) tip.append(html('div', 'viz-tip-title', title));
    for (const [cls, value, label] of rows) {
      const r = html('div', 'viz-tip-row', '');
      r.append(html('i', 'key ' + cls, ''), html('b', '', value), html('span', '', label));
      tip.append(r);
    }
    tip.hidden = false;
    // The tip sits 14px past the pointer, flipping to the other side near a viewport edge.
    const side = (p, size, max) => (p + 14 + size > max - 8 ? p - size - 14 : p + 14);
    tip.style.transform = `translate(${side(ev.clientX, tip.offsetWidth, innerWidth)}px, ${side(ev.clientY, tip.offsetHeight, innerHeight)}px)`;
  }
  function hideTip() {
    tip.hidden = true;
  }
  // Segmented bars are server-rendered HTML: each .seg carries its value and
  // label for the readout.
  let onSeg = false;
  document.addEventListener('pointermove', ev => {
    const seg = ev.target instanceof Element && ev.target.closest('.seg');
    if (seg) {
      onSeg = true;
      showTip(ev, seg.parentElement.dataset.title, [[seg.dataset.c, seg.dataset.v, seg.dataset.k]]);
    } else if (onSeg) {
      onSeg = false;
      hideTip();
    }
  });

  // frame fits an svg to the container and offsets the plot by the margins.
  function frame(el, h, m) {
    const w = el.clientWidth;
    const s = svg('svg', {width: w, height: h, viewBox: `0 0 ${w} ${h}`, role: 'img', 'aria-label': el.dataset.label || ''}, el);
    return {s, m, g: svg('g', {transform: `translate(${m.l},${m.t})`}, s), iw: w - m.l - m.r, ih: h - m.t - m.b};
  }
  const hline = (c, cls, y) => svg('line', {class: cls, x1: 0, x2: c.iw, y1: y, y2: y}, c.g);
  const xTick = (c, x, s) => text(c.g, {class: 'tick', x, y: c.ih + 16, 'text-anchor': 'middle'}, s);
  const line = pts => pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x},${y}`).join('');

  // ticks returns 0 through the first clean step at or above max.
  function ticks(max, n = 3) {
    if (max <= 0) return [0, 1];
    const p = 10 ** Math.floor(Math.log10(max / n));
    const step = [1, 2, 2.5, 5, 10].map(k => k * p).find(s => s >= max / n);
    const out = [];
    for (let v = 0; ; v += step) {
      out.push(v);
      if (v >= max) return out;
    }
  }

  // yAxis grids the plot at the ticks up to max and returns the top one.
  function yAxis(c, max) {
    const tk = ticks(max);
    const top = tk[tk.length - 1];
    for (const v of tk) {
      const y = c.ih - (v / top) * c.ih;
      hline(c, 'grid', y);
      text(c.g, {class: 'tick', x: -8, y: y + 4, 'text-anchor': 'end'}, compact.format(v));
    }
    return top;
  }

  // col draws a column from the baseline: square at the bottom, 4px rounded
  // at the data end.
  function col(x, y, w, h, round) {
    const r = round ? Math.min(4, w / 2, h) : 0;
    return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
  }

  // byDay gap-fills whole UTC days from start to end so the axis is
  // proportional to time.
  function byDay(data) {
    const have = new Map(data.Days.map(d => [d.Day, d]));
    const out = [];
    for (let t = Math.floor(data.Start / DAY) * DAY; t <= data.End; t += DAY) {
      out.push({t, d: have.get(iso(t)) || {}});
    }
    return out;
  }

  // columns: completed builds per day, succeeded under failed.
  function columns(el, data) {
    const days = byDay(data);
    const c = frame(el, +el.dataset.h || 160, {t: 10, r: 4, b: 24, l: 40});
    const {g, iw, ih} = c;
    const top = yAxis(c, Math.max(1, ...days.map(({d}) => (d.Succeeded || 0) + (d.Failed || 0))));
    const band = iw / days.length;
    const bw = Math.max(1, Math.min(24, band - 2));
    c.cur = svg('rect', {class: 'hl', y: 0, height: ih, width: band}, g);
    const bars = svg('g', {class: 'rise'}, g);
    days.forEach(({d}, i) => {
      const x = i * band + (band - bw) / 2;
      const ok = d.Succeeded || 0, bad = d.Failed || 0;
      const hOk = (ok / top) * ih, hBad = (bad / top) * ih;
      if (ok) svg('path', {class: 'ok', d: col(x, ih - hOk, bw, hOk, !bad)}, bars);
      // A 2px surface gap separates the stacked segments.
      if (bad) svg('path', {class: 'bad', d: col(x, ih - hOk - (ok ? 2 : 0) - hBad, bw, hBad, true)}, bars);
    });
    hline(c, 'axis', ih);
    // Day labels at least 64px apart, starting from the first.
    const every = Math.max(1, Math.ceil(64 / band));
    days.forEach(({t}, i) => {
      if (i % every === 0) xTick(c, i * band + band / 2, day(t));
    });
    c.at = px => {
      const i = bin(px, band, days.length);
      c.cur.setAttribute('x', i * band);
      return [day(days[i].t), [
        ['ok', num.format(days[i].d.Succeeded || 0), 'succeeded'],
        ['bad', num.format(days[i].d.Failed || 0), 'failed'],
      ]];
    };
    return c;
  }

  // growth: one ecosystem's running count of packages first reproduced,
  // scaled to its in-scope count so the cards compare as small multiples.
  function growth(el, data) {
    const e = data.Ecos.find(x => x.Ecosystem === el.dataset.eco);
    if (!e) return null;
    const pts = e.Growth || [];
    const last = pts.length ? pts[pts.length - 1][1] : 0;
    const t0 = data.Activity.Start || (pts.length ? pts[0][0] : data.Activity.End);
    const t1 = Math.max(data.Activity.End, t0 + 1);
    const c = frame(el, +el.dataset.h || 110, {t: 16, r: 6, b: 20, l: 6});
    const {g, iw, ih} = c;
    const top = Math.max(e.InScope, last, 1);
    const x = t => ((t - t0) / (t1 - t0)) * iw;
    const y = v => ih - (v / top) * ih;
    hline(c, 'grid', 0);
    text(g, {class: 'tick', x: 0, y: -5}, `${num.format(e.InScope)} in scope`);
    const d = `M0,${ih}` + pts.map(([t, v]) => `H${x(t)}V${y(v)}`).join('') + `H${iw}`;
    const plot = svg('g', {class: 'reveal'}, g);
    svg('path', {class: 'area ok', d: `${d}V${ih}Z`}, plot);
    svg('path', {class: 'line ok', d}, plot);
    svg('circle', {class: 'dot ok', cx: iw, cy: y(last), r: 4}, plot);
    hline(c, 'axis', ih);
    text(g, {class: 'tick', x: 0, y: ih + 15}, day(t0));
    text(g, {class: 'tick', x: iw, y: ih + 15, 'text-anchor': 'end'}, day(t1));
    c.cur = svg('g', {}, g);
    const cl = svg('line', {class: 'cross', y1: 0, y2: ih}, c.cur);
    const cd = svg('circle', {class: 'dot ok', r: 4}, c.cur);
    c.at = px => {
      const t = t0 + (px / iw) * (t1 - t0);
      const v = pts.findLast(p => p[0] <= t)?.[1] || 0;
      attr(cl, {x1: px, x2: px});
      attr(cd, {cx: px, cy: y(v)});
      return [hour(Math.floor(t / HOUR) * HOUR), [['ok', num.format(v), 'reproduced at any version']]];
    };
    return c;
  }

  // spark: one daily measure as a quiet line with the latest day marked.
  function spark(el, data) {
    const days = byDay(data);
    const fmt = formats[el.dataset.fmt] || formats.num;
    const vals = days.map(({d}) => d[el.dataset.field] || 0);
    const c = frame(el, +el.dataset.h || 34, {t: 5, r: 5, b: 3, l: 1});
    const {g, iw, ih} = c;
    const max = Math.max(...vals, 1e-9);
    const step = iw / Math.max(1, vals.length - 1);
    const pts = vals.map((v, i) => [i * step, ih - (v / max) * ih]);
    const d = line(pts);
    svg('path', {class: 'area muted', d: `${d}V${ih}H0Z`}, g);
    svg('path', {class: 'line muted', d}, g);
    const [lx, ly] = pts[pts.length - 1];
    svg('circle', {class: 'dot ok', cx: lx, cy: ly, r: 3.5}, g);
    c.cur = svg('circle', {class: 'dot ok', r: 3.5}, g);
    c.at = px => {
      const i = Math.min(vals.length - 1, Math.round(px / step));
      attr(c.cur, {cx: pts[i][0], cy: pts[i][1]});
      return [day(days[i].t), [['ok', fmt(vals[i]), el.dataset.label || '']]];
    };
    return c;
  }

  // hist: one series of counts over ordered buckets, the value on each cap.
  function hist(el, data) {
    const c = frame(el, +el.dataset.h || 150, {t: 18, r: 4, b: 24, l: 4});
    const {g, iw, ih} = c;
    const max = Math.max(1, ...data.map(b => b.Count));
    const band = iw / data.length;
    const bw = Math.min(56, band - 12);
    c.cur = svg('rect', {class: 'hl', y: 0, height: ih, width: band}, g);
    const bars = svg('g', {class: 'rise'}, g);
    data.forEach((b, i) => {
      const x = i * band + (band - bw) / 2;
      const h = (b.Count / max) * ih;
      svg('path', {class: 'bad', d: col(x, ih - h, bw, Math.max(h, b.Count ? 1 : 0), true)}, bars);
      text(g, {class: 'cap', x: x + bw / 2, y: ih - h - 6, 'text-anchor': 'middle'}, num.format(b.Count));
      xTick(c, x + bw / 2, b.Label);
    });
    hline(c, 'axis', ih);
    c.at = px => {
      const i = bin(px, band, data.length);
      c.cur.setAttribute('x', i * band);
      return [`${data[i].Label} ${el.dataset.unit || ''}`, [['bad', num.format(data[i].Count), el.dataset.label || '']]];
    };
    return c;
  }

  const forms = {columns, growth, spark, hist};
  const parsed = new Map();
  function source(id) {
    if (!parsed.has(id)) parsed.set(id, JSON.parse(document.getElementById(id).textContent));
    return parsed.get(id);
  }
  const drawn = new WeakMap();
  function draw(el, animate) {
    const w = el.clientWidth;
    if (!w || drawn.get(el) === w) return;
    drawn.set(el, w);
    el.replaceChildren();
    const c = forms[el.dataset.chart](el, source(el.dataset.src));
    if (!c) return;
    if (animate) c.s.classList.add('enter');
    // The whole container is the hover area. The pointer's plot position,
    // clamped to the plot, picks what the form reads out.
    c.cur.setAttribute('visibility', 'hidden');
    el.onpointermove = ev => {
      const box = el.getBoundingClientRect();
      const [title, rows] = c.at(clamp(ev.clientX - box.left - c.m.l, 0, c.iw), clamp(ev.clientY - box.top - c.m.t, 0, c.ih));
      c.cur.setAttribute('visibility', 'visible');
      showTip(ev, title, rows);
    };
    el.onpointerleave = () => {
      c.cur.setAttribute('visibility', 'hidden');
      hideTip();
    };
  }
  const charts = [...document.querySelectorAll('[data-chart]')];
  charts.forEach(el => draw(el, true));
  // Redraw at a new width, without the entrance.
  new ResizeObserver(() => charts.forEach(el => draw(el, false))).observe(document.body);
})();

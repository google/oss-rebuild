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
  function yAxis(c, max, fmt = v => compact.format(v)) {
    const tk = ticks(max);
    const top = tk[tk.length - 1];
    for (const v of tk) {
      const y = c.ih - (v / top) * c.ih;
      hline(c, 'grid', y);
      text(c.g, {class: 'tick', x: -8, y: y + 4, 'text-anchor': 'end'}, fmt(v));
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

  // releases: each ecosystem's reproduced rate by release year as a row, a
  // line on a 50-100% scale over each year's version count. Counts are
  // [reproduced, not reproduced, retrying, in flight, queued] per year.
  // Version counts are square-root scaled on one scale across rows, since
  // recent years hold most versions, and points under 30 attempted are faded.
  // Each row's caption sits above it so the plot keeps the full width.
  function releases(el, data) {
    const n = data.Years.length;
    const capH = 18, plotH = 44, gap = 10, rowH = capH + plotH + gap;
    const m = {t: 0, r: 4, b: 20, l: 4};
    const c = frame(el, data.Ecos.length * rowH - gap + m.t + m.b, m);
    const {g, iw, ih} = c;
    const slot = iw / n;
    const attempted = cnt => cnt[0] + cnt[1] + cnt[2];
    const total = cnt => cnt.reduce((a, b) => a + b, 0);
    const vmax = Math.max(1, ...data.Ecos.flatMap(e => e.Counts.map(total)));
    c.cur = svg('rect', {class: 'hl', y: 0, height: ih, width: slot}, g);
    data.Ecos.forEach((e, row) => {
      const top = row * rowH + capH;
      const yRate = r => top + plotH - clamp((r - 0.5) / 0.5, 0, 1) * plotH;
      text(g, {class: 'cap', x: 0, y: top - 6}, e.Ecosystem);
      const sum = e.Counts.reduce((a, cnt) => [a[0] + cnt[0], a[1] + attempted(cnt)], [0, 0]);
      if (sum[1]) text(g, {class: 'tick', x: iw, y: top - 6, 'text-anchor': 'end'}, `${Math.round(100 * sum[0] / sum[1])}% overall`);
      hline(c, 'grid', yRate(1));
      hline(c, 'axis', top + plotH);
      // Each row's columns rise from its own baseline.
      const marks = svg('g', {class: 'rise'}, g);
      e.Counts.forEach((cnt, k) => {
        const v = total(cnt);
        if (!v) return;
        const w = Math.max(2, Math.sqrt(v / vmax) * (slot - 4)), bh = Math.sqrt(v / vmax) * plotH;
        svg('path', {class: 'none', d: col(k * slot + (slot - w) / 2, top + plotH - bh, w, bh, true)}, marks);
      });
      const pts = e.Counts.map((cnt, k) => attempted(cnt) ? [k * slot + slot / 2, yRate(cnt[0] / attempted(cnt)), attempted(cnt) < 30 ? 0.35 : 1] : null);
      const rate = svg('g', {class: 'ok reveal'}, g);
      svg('path', {class: 'line', d: pts.map((p, k) => p ? `${k && pts[k - 1] ? 'L' : 'M'}${p[0]},${p[1]}` : '').join('')}, rate);
      for (const p of pts) if (p) svg('circle', {class: 'dot', cx: p[0], cy: p[1], r: 3.5, opacity: p[2]}, rate);
    });
    // Every other year is labeled, and always the last.
    data.Years.forEach((y, k) => {
      if (k % 2 === 0 || k === n - 1) xTick(c, k * slot + slot / 2, y);
    });
    c.at = (px, py) => {
      const k = bin(px, slot, n);
      const e = data.Ecos[bin(py, rowH, data.Ecos.length)];
      const cnt = e.Counts[k], a = attempted(cnt);
      c.cur.setAttribute('x', k * slot);
      return [`${e.Ecosystem}, released ${data.Years[k]}`, [
        ['ok', a ? `${Math.round(100 * cnt[0] / a)}%` : '–', `of ${num.format(a)} attempted`],
        ['warn', num.format(cnt[1] + cnt[2]), 'not reproduced'],
        ['none', num.format(cnt[3] + cnt[4]), 'not attempted yet'],
      ]];
    };
    return c;
  }

  // series: measures over time against an alert threshold. Data is {Start,
  // End, Step, Fmt, Threshold: {Value, Above}, EndLabels, Series: [{Label,
  // Class, Kind, Points: [[t, v]]}]}. Areas draw under lines and dashed
  // guides. A gap over twice the usual spacing breaks a line. With several
  // group lines the first, the highest, is named at its peak. EndLabels
  // names every series past its last point instead. The threshold is a
  // dashed line with its alerting side shaded. A step of a day or more reads
  // out days rather than hours.
  function series(el, data) {
    const t0 = data.Start;
    const t1 = Math.max(data.End, t0 + 1);
    const fmt = data.Fmt === 'pct'
      ? v => `${(100 * v).toFixed(Math.abs(v) < 0.1 ? 1 : 0)}%`
      : v => (Math.abs(v) >= 100 ? compact.format(v) : String(+v.toPrecision(2)));
    const th = data.Threshold;
    const all = (data.Series || []).filter(sr => sr.Points && sr.Points.length);
    // End labels take a right margin of about 7px a character.
    const named = data.EndLabels ? Math.max(0, ...all.map(sr => sr.Label.length)) * 7 + 12 : 0;
    const c = frame(el, +el.dataset.h || 140, {t: 8, r: 6 + named, b: 22, l: 42});
    const {g, iw, ih} = c;
    const ymax = yAxis(c, Math.max(1e-9, ...all.flatMap(sr => sr.Points.map(p => p[1])), th ? th.Value * 1.15 : 0), fmt);
    const x = t => ((t - t0) / (t1 - t0)) * iw;
    const y = v => ih - (Math.max(0, v) / ymax) * ih;
    if (th) svg('rect', {class: 'zone bad', x: 0, width: iw, y: th.Above ? 0 : y(th.Value), height: th.Above ? y(th.Value) : ih - y(th.Value)}, g);
    const plot = svg('g', {class: 'reveal'}, g);
    // runs splits points into the stretches a line joins: a gap wider than
    // the chart's step, or than the usual spacing without one, has no data.
    const runs = pts => {
      const gaps = pts.slice(1).map((p, i) => p[0] - pts[i][0]).sort((a, b) => a - b);
      const usual = data.Step || (gaps.length ? gaps[gaps.length >> 1] : Infinity);
      const out = [];
      pts.forEach((p, i) => {
        if (i && p[0] - pts[i - 1][0] <= 1.5 * usual) out[out.length - 1].push(p);
        else out.push([p]);
      });
      return out;
    };
    const lineD = run => line(run.map(p => [x(p[0]), y(p[1])]));
    const order = {area: 0, line: 1, dash: 2};
    for (const sr of [...all].sort((a, b) => order[a.Kind] - order[b.Kind])) {
      const rs = runs(sr.Points);
      const grp = svg('g', {class: sr.Class}, plot);
      if (sr.Kind === 'area') svg('path', {class: 'area', d: rs.map(r => `${lineD(r)}V${ih}H${x(r[0][0])}Z`).join('')}, grp);
      svg('path', {class: sr.Kind === 'dash' ? 'line dash' : 'line', d: rs.map(lineD).join('')}, grp);
      // A point with no neighbor to join is a dot.
      for (const r of rs) if (r.length === 1) svg('circle', {class: 'dot', cx: x(r[0][0]), cy: y(r[0][1]), r: 3}, grp);
    }
    if (th) hline(c, 'thresh', y(th.Value));
    const groups = all.filter(sr => sr.Class === 'group');
    if (data.EndLabels) {
      // Each label sits past its line's end, at least 13px from the one above.
      let at = -Infinity;
      all.map(sr => [sr.Label, y(sr.Points[sr.Points.length - 1][1]) + 4])
        .sort((a, b) => a[1] - b[1])
        .forEach(([label, ly]) => {
          at = Math.max(ly, at + 13);
          text(g, {class: 'cap', x: iw + 8, y: at}, label);
        });
    } else if (groups.length > 1) {
      const pk = groups[0].Points.reduce((a, p) => (p[1] > a[1] ? p : a));
      const px = x(pk[0]);
      text(g, {class: 'cap', x: px, y: Math.max(10, y(pk[1]) - 6), 'text-anchor': px > iw * 0.75 ? 'end' : px < iw * 0.25 ? 'start' : 'middle'}, groups[0].Label);
    }
    hline(c, 'axis', ih);
    // Hour labels within two days, day labels past that, 64px apart.
    if (t1 - t0 <= 2 * DAY) {
      const every = ([1, 2, 3, 6, 12].find(h => x(t0 + h * HOUR) >= 64) || 24) * HOUR;
      for (let t = Math.ceil(t0 / every) * every; t <= t1; t += every) xTick(c, x(t), clock(t));
    } else {
      const every = Math.max(1, Math.ceil(64 / x(t0 + DAY))) * DAY;
      for (let t = Math.ceil(t0 / DAY) * DAY; t <= t1; t += every) xTick(c, x(t), day(t));
    }
    const near = (pts, t) => pts.reduce((a, p) => (Math.abs(p[0] - t) <= Math.abs(a[0] - t) ? p : a));
    c.cur = svg('line', {class: 'cross', y1: 0, y2: ih}, g);
    c.at = px => {
      const t = t0 + (px / iw) * (t1 - t0);
      // A point counts as under the cursor within 2% of the range.
      const rows = all.map(sr => [sr, near(sr.Points, t)])
        .filter(([, p]) => Math.abs(p[0] - t) <= (t1 - t0) / 50)
        .sort((a, b) => b[1][1] - a[1][1])
        .slice(0, 6)
        .map(([sr, p]) => [sr.Class, fmt(p[1]), sr.Label]);
      if (th) rows.push(['bad', fmt(th.Value), 'threshold']);
      attr(c.cur, {x1: px, x2: px});
      return [data.Step >= DAY ? day(Math.round(t / DAY) * DAY) : hour(Math.round(t / 60) * 60), rows];
    };
    return c;
  }

  const forms = {columns, growth, spark, hist, releases, series};
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

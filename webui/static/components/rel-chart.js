// An accessible bar chart for an account's "Usage and the picker" (docs/spec/pwa.md 3.0a): an SVG
// with a title, a y axis of round values, a day axis (first, middle, last), gaps with a
// "no data" mark and one colour per model (CSS variables --rel-m1..6, light and dark). The SVG
// is a picture (role="img" with a summary); the days are real buttons laid over it so a
// keyboard, a mouse and a finger all reach the value, which shows in one status line. A
// visually hidden table has the same numbers. Imports nothing, so node can test the layout.

const W = 360;
const H = 168;
const M = { left: 44, right: 8, top: 10, bottom: 24 };
export const PLOT = { w: W - M.left - M.right, h: H - M.top - M.bottom };

// Where everything goes, in viewBox units. `model` is a chart model of relation-fmt.js.
export function layout(model) {
  const n = model.bars.length || 1;
  const slot = PLOT.w / n;
  const bw = Math.min(26, slot * 0.72);
  const y = (frac) => M.top + PLOT.h * (1 - Math.min(1, Math.max(0, frac)));
  const bars = model.bars.map((b, i) => {
    const x = M.left + slot * i + (slot - bw) / 2;
    const gap = b.value == null;
    const height = gap ? 0 : Math.max(2, PLOT.h * b.frac);
    return { x, w: bw, y: M.top + PLOT.h - height, h: height, gap, cx: x + bw / 2, hitLeft: M.left + slot * i, hitW: slot };
  });
  const grid = model.ticks.map((t) => ({ y: y(t.frac), label: t.label }));
  const xs = model.xLabels.map((l, k) => ({ x: bars[l.at] ? bars[l.at].cx : M.left, text: l.text, anchor: k === 0 ? 'start' : k === model.xLabels.length - 1 ? 'end' : 'middle' }));
  // The first and last labels are anchored at the plot edge, not the bar centre, so they stay inside.
  if (xs.length > 1) { xs[0].x = M.left; xs[xs.length - 1].x = W - M.right; }
  const line = model.line ? { y: y(model.line.frac), label: model.line.label } : null;
  return { W, H, bars, grid, xs, line, baseline: y(0) };
}

const NS = 'http://www.w3.org/2000/svg';
function el(tag, attrs, ...kids) {
  const e = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v != null && v !== false) e.setAttribute(k, String(v));
  for (const k of kids) if (k != null) e.append(typeof k === 'object' ? k : document.createTextNode(String(k)));
  return e;
}
function html(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (k === 'style' && typeof v === 'object') for (const [p, val] of Object.entries(v)) e.style.setProperty(p, val);
    else e.setAttribute(k, v === true ? '' : String(v));
  }
  for (const k of kids.flat(Infinity)) if (k != null && k !== false) e.append(typeof k === 'object' ? k : document.createTextNode(String(k)));
  return e;
}

// relChart(model, { key, hint, picked, onPick }) -> an element. `hint`: show the hover/tap hint
// in the status line until a day is picked (the first chart only). `picked`: a remembered day.
export function relChart(model, opts = {}) {
  const g = layout(model);
  const key = opts.key || 'chart';
  const hintText = opts.hint || '';
  const rects = [];
  const bands = [];
  const svgKids = [];
  for (const t of g.grid) {
    svgKids.push(el('line', { class: 'rel-grid', x1: M.left, x2: W - M.right, y1: t.y, y2: t.y }));
    svgKids.push(el('text', { class: 'rel-ytick', x: M.left - 6, y: t.y + 4, 'text-anchor': 'end' }, t.label));
  }
  g.bars.forEach((b, i) => {
    const band = el('rect', { class: 'rel-band', x: b.hitLeft, y: M.top, width: b.hitW, height: PLOT.h });
    bands.push(band);
    svgKids.push(band);
    if (b.gap) {
      // No data: a small hollow mark on the baseline, never blank space.
      rects.push(null);
      svgKids.push(el('rect', { class: 'rel-gap', x: b.x + b.w / 2 - 5, y: g.baseline - 6, width: 10, height: 6, rx: 1.5 }));
    } else {
      const r = el('rect', { class: `rel-bar m${model.bars[i].slot || 1}`, x: b.x, y: b.y, width: b.w, height: b.h, rx: 2 });
      rects.push(r);
      svgKids.push(r);
    }
  });
  if (g.line) {
    svgKids.push(el('line', { class: 'rel-ref', x1: M.left, x2: W - M.right, y1: g.line.y, y2: g.line.y }));
    svgKids.push(el('text', { class: 'rel-reftext', x: W - M.right, y: g.line.y - 4, 'text-anchor': 'end' }, g.line.label));
  }
  svgKids.push(el('line', { class: 'rel-axis', x1: M.left, x2: W - M.right, y1: g.baseline, y2: g.baseline }));
  for (const x of g.xs) svgKids.push(el('text', { class: 'rel-xtick', x: x.x, y: H - 6, 'text-anchor': x.anchor }, x.text));
  const svg = el('svg', { class: 'rel-svg', viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': model.summary, focusable: 'false' }, ...svgKids);

  const status = html('p', { class: `small rel-status${hintText ? ' is-hint' : ''}`, role: 'status', 'aria-live': 'polite', 'data-key': `rel-status-${key}` }, hintText);
  let picked = Number.isInteger(opts.picked) && model.bars[opts.picked] ? opts.picked : -1;
  const buttons = [];
  const show = (i, remember) => {
    bands.forEach((b, k) => b.classList.toggle('is-picked', k === i));
    buttons.forEach((b, k) => b.setAttribute('aria-pressed', String(k === i)));
    status.textContent = model.bars[i].text;
    status.classList.remove('is-hint');
    if (remember) { picked = i; if (opts.onPick) opts.onPick(i); }
  };
  const focusable = () => (picked >= 0 ? picked : 0);
  model.bars.forEach((b, i) => {
    const gl = g.bars[i];
    const btn = html('button', { type: 'button', class: 'rel-day', 'aria-label': b.text, 'aria-pressed': 'false', tabindex: i === focusable() ? '0' : '-1', 'data-day': b.day,
      style: { left: `${(gl.hitLeft / W) * 100}%`, width: `${(gl.hitW / W) * 100}%`, top: `${(M.top / H) * 100}%`, height: `${(PLOT.h / H) * 100}%` },
      onmouseenter: () => show(i, false),
      onfocus: () => show(i, true),
      onclick: () => show(i, true),
      onkeydown: (e) => {
        const to = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? buttons.length - 1 : null;
        if (to == null || !buttons[to]) return;
        e.preventDefault();
        buttons.forEach((x, k) => x.setAttribute('tabindex', k === to ? '0' : '-1'));
        buttons[to].focus();
      } });
    buttons.push(btn);
  });
  const stage = html('div', { class: 'rel-stage' }, svg, html('div', { class: 'rel-days', role: 'group', 'aria-label': `${model.title}: days` }, buttons));

  const legend = [];
  for (const l of model.legend || []) legend.push(html('span', { class: 'rel-key' }, html('span', { class: `rel-sw m${l.slot}`, 'aria-hidden': 'true' }), l.model));
  if (g.bars.some((b) => b.gap)) legend.push(html('span', { class: 'rel-key' }, html('span', { class: 'rel-sw rel-sw-gap', 'aria-hidden': 'true' }), 'no data'));
  const table = html('table', { class: 'sr-only' }, html('caption', {}, model.title),
    html('tbody', {}, model.bars.map((b) => html('tr', {}, html('th', { scope: 'row' }, b.day), html('td', {}, b.text)))));

  const root = html('figure', { class: `rel-chart${model.muted ? ' is-muted' : ''}`, 'data-key': `rel-chart-${key}` },
    html('figcaption', { class: 'rel-cap' }, html('b', { class: 'rel-title' }, model.title), model.sub ? html('span', { class: 'muted small' }, model.sub) : null),
    stage, legend.length ? html('div', { class: 'rel-legend small muted' }, legend) : null, status, table);
  if (picked >= 0) show(picked, false);
  return root;
}

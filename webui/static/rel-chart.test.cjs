// node --test webui/static/*.test.cjs
// The chart layout (components/rel-chart.js): positions, gaps, axis labels. The DOM part is
// covered by the browser smoke (webui/smoke).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/rel-chart.js'), 'utf8');
if (/^import /m.test(src)) throw new Error('rel-chart.js must stay import-free');
const ctx = vm.runInNewContext(`(function () {\n${src.replace(/^export /gm, '')}\nreturn { layout, PLOT };\n})()`, { Math, Object, String, Number, document: {} });
const j = (x) => JSON.parse(JSON.stringify(x));

const model = {
  bars: [{ value: 5, frac: 0.5, slot: 1 }, { value: null, frac: 0, slot: 0 }, { value: 10, frac: 1, slot: 1 }],
  ticks: [{ label: '0%', frac: 0 }, { label: '5%', frac: 0.5 }, { label: '10%', frac: 1 }],
  xLabels: [{ at: 0, text: 'a' }, { at: 1, text: 'b' }, { at: 2, text: 'c' }],
  line: { frac: 0.25, label: 'last week' },
};

test('bars sit on the baseline, gaps have no height, the tallest fills the plot', () => {
  const g = j(ctx.layout(model));
  assert.equal(g.bars.length, 3);
  assert.equal(g.bars[1].gap, true);
  assert.equal(g.bars[1].h, 0);
  assert.equal(g.bars[2].h, ctx.PLOT.h);
  assert.equal(Math.round(g.bars[0].h * 2), Math.round(ctx.PLOT.h));
  assert.equal(g.bars[0].y + g.bars[0].h, g.baseline);
  assert.ok(g.bars[0].x < g.bars[1].x && g.bars[1].x < g.bars[2].x);
});

test('the y axis has a line per tick, bottom to top, and the reference line is placed', () => {
  const g = j(ctx.layout(model));
  assert.deepEqual(g.grid.map((t) => t.label), ['0%', '5%', '10%']);
  assert.ok(g.grid[0].y > g.grid[1].y && g.grid[1].y > g.grid[2].y);
  assert.equal(g.grid[0].y, g.baseline);
  assert.ok(g.line.y < g.baseline && g.line.y > g.grid[1].y);
  assert.equal(ctx.layout({ ...model, line: null }).line, null);
});

test('day labels: first at the start, last at the end, the middle between', () => {
  const g = j(ctx.layout(model));
  assert.deepEqual(g.xs.map((x) => [x.text, x.anchor]), [['a', 'start'], ['b', 'middle'], ['c', 'end']]);
  assert.ok(g.xs[0].x < g.xs[1].x && g.xs[1].x < g.xs[2].x && g.xs[2].x <= g.W);
});

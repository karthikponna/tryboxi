// A copper sky seen through a pixel forest, rasterised into glyph cells.
// Two tree layers move at different speeds for parallax as the footer scrolls in.

export const CELL = 6;
const LEVELS = 7;
// glyph index for the "tryboxi" lettering, drawn past the sky's brightest level
const TEXT = LEVELS;

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hash(x: number, y: number) {
  const h = Math.sin(x * 127.1 + y * 311.7) * 43758.5453;
  return h - Math.floor(h);
}

function noise(x: number, y: number) {
  const xi = Math.floor(x);
  const yi = Math.floor(y);
  const xf = x - xi;
  const yf = y - yi;
  const u = xf * xf * (3 - 2 * xf);
  const v = yf * yf * (3 - 2 * yf);
  const a = hash(xi, yi);
  const b = hash(xi + 1, yi);
  const c = hash(xi, yi + 1);
  const d = hash(xi + 1, yi + 1);
  return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v;
}

function fbm(x: number, y: number) {
  let s = 0;
  let a = 0.5;
  for (let o = 0; o < 4; o++) {
    s += a * noise(x, y);
    x *= 2.1;
    y *= 2.1;
    a *= 0.5;
  }
  return s;
}

const smooth = (a: number, b: number, x: number) => {
  const t = Math.min(1, Math.max(0, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
};

type Layer = { cols: number; rows: number; a: Float32Array };

function layer(cols: number, rows: number): Layer {
  return { cols, rows, a: new Float32Array(cols * rows) };
}

function fill(l: Layer, c: number, r: number, v: number) {
  if (c < 0 || r < 0 || c >= l.cols || r >= l.rows) return;
  const i = r * l.cols + c;
  if (v > l.a[i]) l.a[i] = v;
}

function trunk(l: Layer, x: number, top: number, bottom: number, w: number, lean: number, v: number) {
  for (let r = Math.floor(top); r < bottom; r++) {
    const t = (r - top) / Math.max(1, bottom - top);
    const cx = x + lean * (1 - t);
    const hw = (w * (0.75 + t * 0.35)) / 2;
    for (let c = Math.floor(cx - hw); c <= Math.ceil(cx + hw); c++) fill(l, c, r, v);
  }
}

function blob(l: Layer, cx: number, cy: number, rx: number, ry: number, v: number) {
  for (let r = Math.floor(cy - ry); r <= cy + ry; r++)
    for (let c = Math.floor(cx - rx); c <= cx + rx; c++) {
      const d = ((c - cx) / rx) ** 2 + ((r - cy) / ry) ** 2;
      if (d + (noise(c * 0.35, r * 0.35) - 0.5) * 0.7 < 1) fill(l, c, r, v);
    }
}

function branch(l: Layer, x0: number, y0: number, x1: number, y1: number, w: number, v: number) {
  const n = Math.ceil(Math.hypot(x1 - x0, y1 - y0));
  for (let i = 0; i <= n; i++) {
    const t = i / n;
    const x = x0 + (x1 - x0) * t;
    const y = y0 + (y1 - y0) * t;
    const hw = (w * (1 - t * 0.7)) / 2;
    for (let c = Math.floor(x - hw); c <= x + hw; c++) fill(l, c, Math.round(y), v);
  }
}

function pine(l: Layer, cx: number, base: number, h: number, w: number, v: number) {
  const top = base - h;
  for (let r = Math.floor(top); r <= base; r++) {
    const p = (r - top) / h;
    const tier = (p * 5) % 1;
    const width = w * Math.pow(p, 0.85) * (0.62 + tier * 0.38);
    const jag = (noise(r * 0.9, cx) - 0.5) * 2.2;
    for (let c = Math.floor(cx - width - jag); c <= cx + width + jag; c++) fill(l, c, r, v);
  }
}

export type Forest = { cols: number; rows: number; sky: Float32Array; text: Float32Array; far: Layer; near: Layer };

// "tryboxi" rasterised at cell resolution: coverage 0..1 per cell, sitting on the forest floor.
function lettering(cols: number, rows: number, ground: number, family: string) {
  const c = document.createElement("canvas");
  c.width = cols;
  c.height = rows;
  const g = c.getContext("2d", { willReadFrequently: true });
  const out = new Float32Array(cols * rows);
  if (!g) return out;
  const word = "tryboxi";
  g.font = `800 100px ${family}`;
  const m = g.measureText(word);
  const ascent = m.actualBoundingBoxAscent || 72;
  const descent = m.actualBoundingBoxDescent || 20;
  const target = cols * (cols < 120 ? 0.9 : 0.74);
  const size = Math.min((100 * target) / m.width, (100 * rows * 0.56) / (ascent + descent));
  g.font = `800 ${size}px ${family}`;
  g.textAlign = "center";
  g.textBaseline = "alphabetic";
  g.fillStyle = "#fff";
  // the descender of "y" ends above the ground line, so the parallax floor never swallows it
  g.fillText(word, cols / 2, ground - 8 - (descent * size) / 100);
  const px = g.getImageData(0, 0, cols, rows).data;
  for (let i = 0; i < out.length; i++) out[i] = px[i * 4 + 3] / 255;
  return out;
}

// separable box blur, used to darken the sky in a soft band around the letters
function blur(src: Float32Array, cols: number, rows: number, rad: number) {
  const tmp = new Float32Array(src.length);
  const out = new Float32Array(src.length);
  const n = rad * 2 + 1;
  for (let r = 0; r < rows; r++)
    for (let c = 0; c < cols; c++) {
      let s = 0;
      for (let k = -rad; k <= rad; k++) s += src[r * cols + Math.min(cols - 1, Math.max(0, c + k))];
      tmp[r * cols + c] = s / n;
    }
  for (let r = 0; r < rows; r++)
    for (let c = 0; c < cols; c++) {
      let s = 0;
      for (let k = -rad; k <= rad; k++) s += tmp[Math.min(rows - 1, Math.max(0, r + k)) * cols + c];
      out[r * cols + c] = s / n;
    }
  return out;
}

export function buildForest(width: number, height: number, family = "sans-serif"): Forest {
  const cols = Math.ceil(width / CELL);
  const rows = Math.ceil(height / CELL);
  const pad = 16;
  const rand = rng(11);
  const sky = new Float32Array(cols * rows);
  const ground = rows * 0.9;
  for (let r = 0; r < rows; r++) {
    const v = r / rows;
    for (let c = 0; c < cols; c++) {
      const u = c / cols;
      const vertical = 0.3 + smooth(0.12, 0.7, v) * 0.7;
      const side = 1 - Math.pow(Math.abs(u - 0.5) * 2, 2.6) * 0.6;
      const horizon = Math.exp(-((v - 0.74) ** 2) / 0.012) * 0.22;
      const haze = (fbm(c * 0.022, r * 0.07) - 0.5) * 0.3;
      let s = vertical * side + horizon + haze;
      if (r > ground) s *= Math.max(0, 1 - (r - ground) / 2);
      sky[r * cols + c] = Math.max(0, Math.min(1, s));
    }
  }

  const text = lettering(cols, rows, ground, family);
  const halo = blur(text, cols, rows, 3);
  for (let i = 0; i < sky.length; i++) sky[i] *= 1 - Math.min(1, halo[i] * 1.6) * 0.7;

  // tree sizes are tuned for a 1440px-wide footer and shrink with it
  const k = Math.min(1, Math.max(0.3, cols / 240));

  // far layer: a few hazy trunks and soft crowns
  const far = layer(cols, rows + pad);
  for (let i = 0; i < 9; i++) {
    const x = (0.06 + i * 0.11 + (rand() - 0.5) * 0.05) * cols;
    const top = rows * (0.25 + rand() * 0.15);
    trunk(far, x, top, rows + pad, Math.max(1, (1.2 + rand() * 1.8) * k), (rand() - 0.5) * 4 * k, 0.55);
    blob(far, x + (rand() - 0.5) * 8 * k, top, (6 + rand() * 10) * k, (4 + rand() * 6) * k, 0.55);
  }

  // near layer: canopy with holes, heavy trunks, foreground pines, forest floor
  const near = layer(cols, rows + pad);
  const edge = (c: number) => rows * (0.14 + fbm(c * 0.016, 2.0) * 0.3);
  for (let c = 0; c < cols; c++) {
    const e = edge(c);
    for (let r = 0; r < e + rows * 0.08; r++) {
      const solid = r < e * 0.55;
      const leaves = fbm(c * 0.075, r * 0.11) + (1 - r / e) * 0.35;
      if (solid || leaves > 0.62) fill(near, c, r, 1);
    }
  }
  const trunks = [0.03, 0.2, 0.41, 0.67, 0.88];
  for (const p of trunks) {
    const x = p * cols + (rand() - 0.5) * 6 * k;
    const w = Math.max(1.5, (4 + rand() * 4) * k);
    const lean = (rand() - 0.5) * 8 * k;
    trunk(near, x, edge(Math.round(x)) * 0.6, ground + 4, w, lean, 1);
    for (let b = 0; b < 3; b++) {
      const y0 = edge(Math.round(x)) + rows * (0.05 + rand() * 0.18);
      const dir = rand() < 0.5 ? -1 : 1;
      const x1 = x + dir * (8 + rand() * 16) * k;
      const y1 = y0 - (6 + rand() * 12) * k;
      branch(near, x + lean * 0.3, y0, x1, y1, Math.max(1, w * 0.35), 1);
      blob(near, x1, y1, (5 + rand() * 7) * k, (3 + rand() * 4) * k, 1);
    }
  }
  const pines = [
    [0.09, 0.42],
    [0.31, 0.3],
    [0.56, 0.24],
    [0.77, 0.38],
    [0.96, 0.46],
  ];
  for (const [p, h] of pines) pine(near, p * cols + (rand() - 0.5) * 4 * k, ground + 2, rows * h, (9 + rand() * 6) * k, 1);
  for (let c = 0; c < cols; c++) {
    const lip = ground - (noise(c * 0.2, 4.4) - 0.3) * 3;
    for (let r = Math.floor(lip); r < rows + pad; r++) fill(near, c, r, 1);
  }
  return { cols, rows, sky, text, far, near };
}

function fbm3(x: number, y: number) {
  return noise(x, y) * 0.57 + noise(x * 2.1, y * 2.1) * 0.29 + noise(x * 4.4, y * 4.4) * 0.14;
}

// Intensity per cell after the tree layers are offset for parallax. `t` (seconds) drives
// the fire: a warped noise field streams right to left behind the trees, rising slightly.
// The lettering sits between the sky and the trees, and wisps of smoke drift across it.
export function compose(f: Forest, farShift: number, nearShift: number, t: number, out: Uint8Array) {
  const { cols, rows, sky, text, far, near } = f;
  const bayer = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5];
  const drift = t * 0.42;
  for (let r = 0; r < rows; r++) {
    const fr = Math.min(far.rows - 1, Math.max(0, r + farShift));
    const nr = Math.min(near.rows - 1, Math.max(0, r + nearShift));
    const v01 = r / rows;
    const lift = smooth(0.25, 0.95, v01);
    for (let c = 0; c < cols; c++) {
      const i = r * cols + c;
      const cover = near.a[nr * cols + c];
      if (cover >= 1) {
        out[i] = 0;
        continue;
      }
      const warp = noise(c * 0.03 + drift * 1.6, r * 0.05 + t * 0.35);
      const n = fbm3(c * 0.017 + drift, r * 0.075 + t * 0.22 + warp * 0.9);
      const flame = smooth(0.36, 0.74, n);
      const haze = far.a[fr * cols + c];
      const b = bayer[(r & 3) * 4 + (c & 3)] / 16 - 0.5;
      const m = text[i];
      // the lettering stands in front of the hazy far trees, behind the near ones
      if (m > 0 && cover <= 0) {
        const smoke = smooth(0.56, 0.84, fbm3(c * 0.05 + drift * 1.3 + 17.3, r * 0.12 + t * 0.3 + warp * 0.7));
        if (m * (1 - smoke * 0.9) > 0.5 + b * 0.6) {
          out[i] = TEXT;
          continue;
        }
      }
      let v = sky[i] * (0.5 + flame * 0.8) + flame * lift * 0.22;
      v *= 1 - haze * 0.8;
      v *= 1 - cover;
      const d = b / LEVELS;
      out[i] = Math.max(0, Math.min(LEVELS - 1, Math.round((v + d) * (LEVELS - 1))));
    }
  }
}

// Glyph tiles, darkest to brightest: faint grid, dots, cross, double cross, shade, dot-matrix, solid,
// then the lettering's hot "#" glyph.
export function glyphAtlas(dpr: number) {
  const s = CELL * dpr;
  const c = document.createElement("canvas");
  c.width = s * (LEVELS + 1);
  c.height = s;
  const g = c.getContext("2d")!;
  const px = (x: number, y: number, w: number, h: number, col: string) => {
    g.fillStyle = col;
    g.fillRect(Math.round(x * dpr), Math.round(y * dpr), Math.max(1, Math.round(w * dpr)), Math.max(1, Math.round(h * dpr)));
  };
  const at = (i: number) => i * CELL;
  px(at(0) + 5, 0, 1, 6, "rgba(120,60,24,0.18)");
  px(at(0), 5, 6, 1, "rgba(120,60,24,0.18)");
  px(at(1) + 2, 2, 1, 1, "#5a2c10");
  px(at(1) + 5, 5, 1, 1, "#4a240d");
  px(at(2) + 2, 0, 1, 6, "#6e3513");
  px(at(2), 2, 6, 1, "#6e3513");
  px(at(3), 0, 6, 6, "#4a240d");
  px(at(3) + 1, 0, 1, 6, "#9a4a1a");
  px(at(3) + 4, 0, 1, 6, "#9a4a1a");
  px(at(3), 1, 6, 1, "#9a4a1a");
  px(at(3), 4, 6, 1, "#9a4a1a");
  px(at(4), 0, 6, 6, "#a14f1c");
  for (let y = 0; y < 6; y += 2) for (let x = (y / 2) % 2; x < 6; x += 2) px(at(4) + x, y, 1, 1, "#6a3313");
  px(at(5), 0, 6, 6, "#d06a28");
  px(at(5) + 1, 1, 1, 1, "#8d4318");
  px(at(5) + 3, 1, 1, 1, "#8d4318");
  px(at(6), 0, 6, 6, "#e27a38");
  px(at(6) + 1, 1, 1, 1, "#b0561f");
  px(at(TEXT), 0, 6, 6, "#ee8c50");
  px(at(TEXT) + 1, 0, 1, 6, "#ffc596");
  px(at(TEXT) + 4, 0, 1, 6, "#ffc596");
  px(at(TEXT), 1, 6, 1, "#ffc596");
  px(at(TEXT), 4, 6, 1, "#ffc596");
  return c;
}

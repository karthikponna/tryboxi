// The iMac's screen: a 2D canvas used as a texture. Each window is a sandbox microVM
// running a short script; windows tile like a tiling window manager as more spawn.

export const SCREEN_W = 1280;
export const SCREEN_H = 720;
const MENU_H = 30;
const PAD = 22;
const GAP = 16;
const MAX_WINDOWS = 9;
const WIN_W = 520;
const WIN_H = 300;
const TYPE_MS = 38;

type LineKind = "cmd" | "ok" | "err" | "out";
type Line = { k: LineKind; t: string };
type Rect = { x: number; y: number; w: number; h: number };

const SCRIPTS: [string, LineKind, string][] = [
  ["npm test", "ok", "12 passed"],
  ["docker run -d nginx", "ok", "nginx up on :8080"],
  ["pip install -r requirements.txt", "ok", "41 packages"],
  ["go test ./...", "out", "ok  ./...  0.8s"],
  ["curl evil.example", "err", "blocked by egress policy"],
  ["uname -sr", "out", "Linux 6.12"],
];

type Win = {
  id: number;
  title: string;
  born: number;
  closing: number;
  rect: Rect;
  target: Rect;
  lines: Line[];
  script: number;
  phase: 0 | 1;
  typeAt: number;
  nextAt: number;
};

const hex = () => Math.floor(Math.random() * 4096).toString(16).padStart(3, "0");

function layout(n: number): Rect[] {
  const cols = n <= 1 ? 1 : n <= 4 ? 2 : 3;
  const rows = Math.max(1, Math.ceil(n / cols));
  const top = MENU_H + PAD;
  const w = (SCREEN_W - PAD * 2 - GAP * (cols - 1)) / cols;
  const h = (SCREEN_H - top - PAD - GAP * (rows - 1)) / rows;
  const out: Rect[] = [];
  for (let i = 0; i < n; i++) {
    const r = Math.floor(i / cols);
    const c = i % cols;
    const inRow = Math.min(cols, n - r * cols);
    const offset = ((cols - inRow) * (w + GAP)) / 2;
    out.push({ x: PAD + offset + c * (w + GAP), y: top + r * (h + GAP), w, h });
  }
  return out;
}

export class Screen {
  canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private wins: Win[] = [];
  private nextId = 1;
  private now = 0;
  private hint = true;
  private font: string;
  private sans: string;
  private reduced: boolean;

  constructor(opts: { mono: string; sans: string; reduced: boolean }) {
    this.canvas = document.createElement("canvas");
    this.canvas.width = SCREEN_W;
    this.canvas.height = SCREEN_H;
    this.ctx = this.canvas.getContext("2d")!;
    this.font = opts.mono;
    this.sans = opts.sans;
    this.reduced = opts.reduced;
  }

  get count() {
    return this.wins.filter((w) => !w.closing).length;
  }

  spawn(now: number, user: boolean) {
    if (user) this.hint = false;
    const live = this.wins.filter((w) => !w.closing);
    if (live.length >= MAX_WINDOWS) {
      const oldest = live[0];
      oldest.closing = now;
      if (this.reduced) this.wins = this.wins.filter((w) => w !== oldest);
    }
    const id = this.nextId++;
    const win: Win = {
      id,
      title: id === 1 ? "sbx_7f3" : `sbx_${hex()}`,
      born: this.reduced ? -1e9 : now,
      closing: 0,
      rect: { x: SCREEN_W / 2, y: SCREEN_H / 2, w: 0, h: 0 },
      target: { x: 0, y: 0, w: 0, h: 0 },
      lines: [{ k: "cmd", t: "sandboxctl create -w ." }],
      script: (id - 1) % SCRIPTS.length,
      phase: 1,
      typeAt: this.reduced ? -1e9 : now,
      nextAt: now + 700,
    };
    if (this.reduced) {
      const [cmd, k, out] = SCRIPTS[win.script];
      win.lines.push({ k: "ok", t: "ready in 32 ms from the warm pool" }, { k: "cmd", t: cmd }, { k, t: out });
      win.phase = 0;
    }
    this.wins.push(win);
    this.relayout(true);
    return win.title;
  }

  private relayout(snapNew: boolean) {
    const live = this.wins.filter((w) => !w.closing);
    const rects = layout(live.length);
    live.forEach((w, i) => {
      w.target = rects[i];
      const fresh = w.rect.w === 0;
      if (this.reduced) w.rect = { ...rects[i] };
      else if (fresh && snapNew) {
        const t = rects[i];
        w.rect = { x: t.x + t.w / 2, y: t.y + t.h / 2, w: 0, h: 0 };
      }
    });
  }

  private step(w: Win) {
    if (w.phase === 0) {
      const [cmd] = SCRIPTS[w.script];
      w.lines.push({ k: "cmd", t: cmd });
      w.typeAt = this.now;
      w.nextAt = this.now + TYPE_MS * cmd.length + 500 + Math.random() * 500;
      w.phase = 1;
    } else {
      const last = w.lines[w.lines.length - 1];
      if (last.t === "sandboxctl create -w .") w.lines.push({ k: "ok", t: "ready in 32 ms from the warm pool" });
      else {
        const [, k, out] = SCRIPTS[w.script];
        w.lines.push({ k, t: out });
        w.script = (w.script + 1 + Math.floor(Math.random() * 3)) % SCRIPTS.length;
      }
      w.phase = 0;
      w.nextAt = this.now + 1100 + Math.random() * 1400;
    }
    if (w.lines.length > 12) w.lines.splice(0, w.lines.length - 12);
  }

  // Advances animation; returns true when the canvas needs a redraw.
  tick(now: number) {
    this.now = now;
    if (this.reduced) return false;
    let dirty = false;
    for (const w of this.wins) {
      if (!w.closing && now >= w.nextAt) {
        this.step(w);
        dirty = true;
      }
      const k = 0.18;
      const r = w.rect;
      const t = w.target;
      if (Math.abs(r.x - t.x) + Math.abs(r.y - t.y) + Math.abs(r.w - t.w) + Math.abs(r.h - t.h) > 0.5) {
        r.x += (t.x - r.x) * k;
        r.y += (t.y - r.y) * k;
        r.w += (t.w - r.w) * k;
        r.h += (t.h - r.h) * k;
        dirty = true;
      }
      const last = w.lines[w.lines.length - 1];
      if (last?.k === "cmd" && now - w.typeAt < TYPE_MS * (last.t.length + 1)) dirty = true;
    }
    const before = this.wins.length;
    this.wins = this.wins.filter((w) => !w.closing || now - w.closing < 260);
    if (this.wins.length !== before) {
      this.relayout(false);
      dirty = true;
    }
    if (this.wins.some((w) => w.closing)) dirty = true;
    return dirty;
  }

  draw() {
    const c = this.ctx;
    const g = c.createLinearGradient(0, 0, 0, SCREEN_H);
    g.addColorStop(0, "#07100b");
    g.addColorStop(1, "#030504");
    c.fillStyle = g;
    c.fillRect(0, 0, SCREEN_W, SCREEN_H);
    const glow = c.createRadialGradient(SCREEN_W * 0.72, SCREEN_H * 0.95, 0, SCREEN_W * 0.72, SCREEN_H * 0.95, SCREEN_W * 0.7);
    glow.addColorStop(0, "rgba(62,227,141,0.22)");
    glow.addColorStop(1, "rgba(62,227,141,0)");
    c.fillStyle = glow;
    c.fillRect(0, 0, SCREEN_W, SCREEN_H);

    c.fillStyle = "rgba(0,0,0,0.55)";
    c.fillRect(0, 0, SCREEN_W, MENU_H);
    c.textBaseline = "middle";
    c.font = `600 15px ${this.sans}`;
    c.fillStyle = "#f2f6f0";
    c.fillText("boxi", 18, MENU_H / 2 + 1);
    c.font = `400 14px ${this.sans}`;
    c.fillStyle = "rgba(242,246,240,0.6)";
    c.fillText("sandboxd", 62, MENU_H / 2 + 1);
    c.font = `400 13px ${this.font}`;
    const status = `${this.count} microVM${this.count === 1 ? "" : "s"} running · $0`;
    c.textAlign = "right";
    c.fillStyle = "rgba(242,246,240,0.75)";
    c.fillText(status, SCREEN_W - 18, MENU_H / 2 + 1);
    c.textAlign = "left";

    if (this.hint) {
      c.font = `400 13px ${this.font}`;
      c.textAlign = "center";
      c.fillStyle = "#3ee38d";
      c.fillText("click the iMac to start another microVM", SCREEN_W / 2, MENU_H / 2 + 1);
      c.textAlign = "left";
    }

    for (const w of this.wins) this.drawWin(w);
  }

  private drawWin(w: Win) {
    const c = this.ctx;
    const { x, y, w: rw, h: rh } = w.rect;
    if (rw < 8 || rh < 8) return;
    const fade = w.closing ? Math.max(0, 1 - (this.now - w.closing) / 260) : 1;
    const s = Math.min(rw / WIN_W, rh / WIN_H);
    c.save();
    c.globalAlpha = fade;
    c.translate(x, y);
    roundRect(c, 0, 0, rw, rh, 12 * Math.min(1, s * 1.4));
    c.fillStyle = "#0b0e0c";
    c.fill();
    c.strokeStyle = "rgba(226,240,230,0.2)";
    c.lineWidth = 1.5;
    c.stroke();
    c.clip();

    const bar = Math.max(18, 34 * s);
    c.fillStyle = "#121613";
    c.fillRect(0, 0, rw, bar);
    const dots = ["#ff5f57", "#febc2e", "#28c840"];
    dots.forEach((col, i) => {
      c.beginPath();
      c.arc(14 * s + 6 + i * 18 * s, bar / 2, Math.max(3, 5.5 * s), 0, Math.PI * 2);
      c.fillStyle = col;
      c.fill();
    });
    c.font = `500 ${Math.max(9, 15 * s)}px ${this.font}`;
    c.textBaseline = "middle";
    c.textAlign = "center";
    c.fillStyle = "rgba(242,246,240,0.62)";
    c.fillText(`${w.title} · ubuntu 24.04`, rw / 2, bar / 2 + 1);
    c.textAlign = "left";

    const fs = Math.max(9, 19 * s);
    const lh = fs * 1.55;
    const foot = Math.max(18, 30 * s);
    const bodyTop = bar + 10 * s + lh / 2;
    const maxLines = Math.max(1, Math.floor((rh - bar - foot - 14 * s) / lh));
    const shown = w.lines.slice(-maxLines);
    c.font = `400 ${fs}px ${this.font}`;
    shown.forEach((l, i) => {
      const ly = bodyTop + i * lh;
      const lx = 16 * s + 4;
      const newest = i === shown.length - 1;
      let text = l.t;
      if (newest && l.k === "cmd") text = text.slice(0, Math.max(0, Math.floor((this.now - w.typeAt) / TYPE_MS)));
      const prefix = l.k === "cmd" ? "$ " : l.k === "ok" ? "✓ " : l.k === "err" ? "✗ " : "";
      c.fillStyle = l.k === "cmd" ? "rgba(242,246,240,0.5)" : l.k === "ok" ? "#3ee38d" : "#ff6b6b";
      if (prefix) c.fillText(prefix, lx, ly);
      const px = prefix ? c.measureText(prefix).width : 0;
      c.fillStyle = l.k === "out" ? "rgba(242,246,240,0.64)" : l.k === "err" ? "#ff6b6b" : "#f2f6f0";
      c.fillText(text, lx + px, ly);
      if (newest && l.k === "cmd" && Math.floor(this.now / 530) % 2 === 0) {
        c.fillStyle = "#3ee38d";
        c.fillRect(lx + px + c.measureText(text).width + 2, ly - fs * 0.55, fs * 0.5, fs * 1.1);
      }
    });

    c.fillStyle = "rgba(255,255,255,0.03)";
    c.fillRect(0, rh - foot, rw, foot);
    c.font = `400 ${Math.max(8, 14 * s)}px ${this.font}`;
    c.fillStyle = "#3ee38d";
    c.fillText("● running", 16 * s + 4, rh - foot / 2);
    c.textAlign = "right";
    c.fillStyle = "rgba(242,246,240,0.45)";
    c.fillText("2 vCPU · 2 GB", rw - 16 * s - 4, rh - foot / 2);
    c.textAlign = "left";
    c.restore();
  }
}

function roundRect(c: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  c.beginPath();
  c.moveTo(x + r, y);
  c.arcTo(x + w, y, x + w, y + h, r);
  c.arcTo(x + w, y + h, x, y + h, r);
  c.arcTo(x, y + h, x, y, r);
  c.arcTo(x, y, x + w, y, r);
  c.closePath();
}

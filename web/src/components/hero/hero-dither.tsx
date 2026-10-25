"use client";

import { useEffect, useRef } from "react";

const CELL = 8;
// ember → flame, darkest first; 0 in the cell state means "off" (black)
const HEAT = ["#2a1308", "#4e2210", "#8a3c16", "#c4612a", "#e27a38", "#f4a46c"];
const FRAME_MS = 66;
const BAYER = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => v / 16 - 0.5);

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
  let sum = 0;
  let amp = 0.5;
  for (let o = 0; o < 4; o++) {
    sum += amp * noise(x, y);
    x *= 2.03;
    y *= 2.03;
    amp *= 0.5;
  }
  return sum;
}

type Ball = { x: number; y: number; r: number; vx: number; vy: number; tail: number; seed: number };

// Pure black with an 8px dither that grows out of the top-right corner, shaded as fire licking right to left,
// and throwing fireballs that fly off to the left.
export function HeroDither({ top = 64 }: { top?: number }) {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let cols = 0;
    let rows = 0;
    let w = 0;
    let h = 0;
    let state = new Uint8Array(0);
    let glow = new Float32Array(0);
    const balls: Ball[] = [];
    let nextBall = 0.4;
    let t = 0;
    let raf = 0;
    let last = 0;
    let visible = true;

    const resize = () => {
      const r = canvas.getBoundingClientRect();
      const dpr = Math.min(2, window.devicePixelRatio || 1);
      w = Math.round(r.width);
      h = Math.round(r.height);
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      cols = Math.ceil(w / CELL);
      rows = Math.ceil(h / CELL);
      state = new Uint8Array(cols * rows).fill(255);
      glow = new Float32Array(cols * rows);
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, w, h);
      draw();
    };

    // fireballs shrink and thin out on narrow screens
    const scale = () => Math.min(1, Math.max(0.45, w / 1200));
    const launch = (x = w - Math.random() * Math.min(260, w * 0.2)) => {
      const k = scale();
      const r = (16 + Math.random() * 30) * k;
      balls.push({
        x,
        // mostly along the top, where the corner fire burns
        y: Math.pow(Math.random(), 1.25) * h * 0.9,
        r,
        vx: (90 + Math.random() * 110 + (46 - r / k) * 1.5) * Math.sqrt(k),
        vy: -4 - Math.random() * 10,
        tail: 3 + Math.random() * 3,
        seed: Math.random() * 100,
      });
    };

    // Splat each fireball into `glow`: a round head with a tail streaming back to the right.
    const stepBalls = (dt: number) => {
      glow.fill(0);
      nextBall -= dt;
      if (nextBall <= 0 && balls.length < Math.round(14 * scale())) {
        launch();
        nextBall = (0.25 + Math.random() * 0.5) / scale();
      }
      for (let k = balls.length - 1; k >= 0; k--) {
        const b = balls[k];
        b.x -= b.vx * dt;
        b.y += (b.vy + Math.sin(t * 3 + b.seed) * 14) * dt;
        const len = b.r * b.tail;
        if (b.x + len < 0) {
          balls.splice(k, 1);
          continue;
        }
        // burn down as it crosses the page
        const life = Math.min(1, Math.max(0, b.x / (w * 0.7)) ** 0.4 + 0.25);
        const c0 = Math.max(0, Math.floor((b.x - b.r) / CELL));
        const c1 = Math.min(cols - 1, Math.ceil((b.x + len) / CELL));
        const r0 = Math.max(0, Math.floor((b.y - b.r) / CELL));
        const r1 = Math.min(rows - 1, Math.ceil((b.y + b.r) / CELL));
        for (let cy = r0; cy <= r1; cy++) {
          for (let cx = c0; cx <= c1; cx++) {
            const dx = (cx + 0.5) * CELL - b.x;
            const dy = (cy + 0.5) * CELL - b.y;
            let d: number;
            if (dx > 0) {
              const u = dx / len;
              // the tail thins out and flickers behind the head
              const width = b.r * (1 - u * 0.85) * (0.8 + noise(cx * 0.4 + b.seed, t * 6) * 0.4);
              d = Math.max(u, Math.abs(dy) / Math.max(1, width));
            } else {
              d = Math.hypot(dx, dy) / b.r;
            }
            if (d >= 1) continue;
            const g = (1 - d) * life;
            const i = cy * cols + cx;
            if (g > glow[i]) glow[i] = g;
          }
        }
      }
    };

    const draw = () => {
      const reachX = Math.min(Math.max(w * 0.62, 200), 900);
      const reachY = Math.min(Math.max(h * 0.95, 200), 640, w * 0.7);
      const band = Math.min(88, w * 0.1);
      for (let cy = 0; cy < rows; cy++) {
        for (let cx = 0; cx < cols; cx++) {
          const px = w - (cx + 0.5) * CELL;
          const py = (cy + 0.5) * CELL;
          const d = Math.hypot(px / reachX, py / reachY);
          const corner = 1 - Math.min(1, Math.max(0, (d - 0.12) / 0.88));
          const edge = px < band ? (1 - px / band) * 0.75 * Math.max(0, 1 - py / (reachY * 1.6)) : 0;
          const n = fbm(cx * 0.085 + t * 0.6, cy * 0.24 + t * 0.25);
          const v = corner * corner * 1.15 + edge + (n - 0.5) * 1.1;
          const b = BAYER[(cy & 3) * 4 + (cx & 3)];
          const i = cy * cols + cx;
          const g = glow[i];
          let s = 0;
          if (v > 0.55 + b * 0.22 || g > 0.12 + b * 0.2) {
            // sampling further right as t grows moves every flame tongue leftward, and up
            const f = fbm(cx * 0.05 + t * 1.5, cy * 0.12 + t * 0.7);
            const heat = Math.max(
              v > 0.55 + b * 0.22 ? Math.min(0.5, v - 0.55) * 0.55 + (f - 0.5) * 2.6 + b * 0.45 : 0,
              g * 1.25 + (f - 0.5) * 0.6 + b * 0.3,
            );
            s = 1 + Math.max(0, Math.min(HEAT.length - 1, Math.floor(heat * HEAT.length)));
          }
          if (state[i] === s) continue;
          state[i] = s;
          ctx.fillStyle = s ? HEAT[s - 1] : "#000";
          ctx.fillRect(cx * CELL, cy * CELL, CELL, CELL);
        }
      }
    };

    const loop = (now: number) => {
      raf = requestAnimationFrame(loop);
      if (now - last < FRAME_MS) return;
      const dt = last ? Math.min(0.1, (now - last) / 1000) : FRAME_MS / 1000;
      last = now;
      t += 0.024;
      stepBalls(dt);
      draw();
    };
    const start = () => {
      if (raf || reduced || !visible || document.hidden) return;
      if (!balls.length) for (let k = 0; k < Math.round(7 * scale()); k++) launch(w * (0.15 + Math.random() * 0.8));
      raf = requestAnimationFrame(loop);
    };
    const stop = () => {
      cancelAnimationFrame(raf);
      raf = 0;
    };

    resize();
    start();
    const ro = new ResizeObserver(resize);
    ro.observe(canvas);
    const io = new IntersectionObserver(([e]) => {
      visible = e.isIntersecting;
      if (visible) start();
      else stop();
    });
    io.observe(canvas);
    const onVis = () => (document.hidden ? stop() : start());
    document.addEventListener("visibilitychange", onVis);
    return () => {
      stop();
      ro.disconnect();
      io.disconnect();
      document.removeEventListener("visibilitychange", onVis);
    };
  }, []);

  return (
    <canvas
      ref={ref}
      aria-hidden="true"
      className="pointer-events-none absolute inset-x-0 w-full"
      style={{ top, height: `calc(100% - ${top}px)` }}
    />
  );
}

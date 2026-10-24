"use client";

import { useEffect, useRef } from "react";

const CELL = 8;
const ON = "#3a3a3a";
const FRAME_MS = 100;
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

// Pure black with a binary 8px dither that grows out of the top-right corner.
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
      state = new Uint8Array(cols * rows).fill(2);
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, w, h);
      draw();
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
          const n = fbm(cx * 0.085 + t * 0.6, cy * 0.24 - t * 0.25);
          const v = corner * corner * 1.15 + edge + (n - 0.5) * 1.1;
          const on = v > 0.55 + BAYER[(cy & 3) * 4 + (cx & 3)] * 0.22 ? 1 : 0;
          const i = cy * cols + cx;
          if (state[i] === on) continue;
          state[i] = on;
          ctx.fillStyle = on ? ON : "#000";
          ctx.fillRect(cx * CELL, cy * CELL, CELL, CELL);
        }
      }
    };

    const loop = (now: number) => {
      raf = requestAnimationFrame(loop);
      if (now - last < FRAME_MS) return;
      last = now;
      t += 0.035;
      draw();
    };
    const start = () => {
      if (raf || reduced || !visible || document.hidden) return;
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

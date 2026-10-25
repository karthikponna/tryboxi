"use client";

import { useEffect, useRef } from "react";
import { buildForest, CELL, compose, glyphAtlas } from "./forest";

const FRAME_MS = 1000 / 15;

export function FooterForest() {
  const ref = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = ref.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let forest: ReturnType<typeof buildForest> | null = null;
    let atlas: HTMLCanvasElement | null = null;
    let cur = new Uint8Array(0);
    let prev = new Uint8Array(0);
    let dpr = 1;
    let raf = 0;
    let last = 0;
    let clock = 0;
    let built = false;
    let visible = false;
    const family = `${getComputedStyle(document.documentElement).getPropertyValue("--font-display").trim() || "system-ui"}, system-ui, sans-serif`;
    let fontReady = false;

    const shifts = () => {
      if (reduced) return [0, 0];
      const r = canvas.getBoundingClientRect();
      const p = Math.min(1, Math.max(0, (window.innerHeight - r.top) / (window.innerHeight + r.height)));
      return [Math.round((1 - p) * 6), Math.round((1 - p) * 14)];
    };

    const paint = () => {
      if (!forest || !atlas) return;
      const [far, near] = shifts();
      compose(forest, far, near, clock, cur);
      const s = CELL * dpr;
      for (let i = 0; i < cur.length; i++) {
        if (cur[i] === prev[i]) continue;
        const x = (i % forest.cols) * s;
        const y = Math.floor(i / forest.cols) * s;
        ctx.clearRect(x, y, s, s);
        ctx.drawImage(atlas, cur[i] * s, 0, s, s, x, y, s, s);
      }
      [prev, cur] = [cur, prev];
    };

    const build = () => {
      const r = canvas.getBoundingClientRect();
      dpr = Math.min(2, Math.round(window.devicePixelRatio || 1));
      const w = Math.round(r.width);
      const h = Math.round(r.height);
      canvas.width = Math.ceil(w / CELL) * CELL * dpr;
      canvas.height = Math.ceil(h / CELL) * CELL * dpr;
      forest = buildForest(w, h, family);
      atlas = glyphAtlas(dpr);
      cur = new Uint8Array(forest.cols * forest.rows);
      prev = new Uint8Array(forest.cols * forest.rows).fill(255);
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      built = true;
      paint();
    };

    const loop = (now: number) => {
      raf = requestAnimationFrame(loop);
      const dt = now - last;
      if (dt < FRAME_MS - 2) return;
      last = now;
      clock += Math.min(dt, 200) / 1000;
      paint();
    };
    const start = () => {
      if (raf || reduced || !visible || document.hidden) return;
      last = performance.now();
      raf = requestAnimationFrame(loop);
    };
    const stop = () => {
      cancelAnimationFrame(raf);
      raf = 0;
    };

    const io = new IntersectionObserver(
      ([e]) => {
        visible = e.isIntersecting;
        if (visible && !built && !fontReady) {
          fontReady = true;
          // the lettering is rasterised once, so wait for the heavy display weight first
          document.fonts
            .load(`800 64px ${family}`)
            .catch(() => undefined)
            .then(() => {
              build();
              if (visible) start();
            });
          return;
        }
        if (visible) start();
        else stop();
      },
      { rootMargin: "200px 0px" },
    );
    io.observe(canvas);
    let width = 0;
    const ro = new ResizeObserver(([e]) => {
      const w = Math.round(e.contentRect.width);
      if (!built || w === width) return;
      width = w;
      build();
    });
    ro.observe(canvas);
    const onScroll = () => {
      if (reduced && built) paint();
    };
    const onVis = () => (document.hidden ? stop() : start());
    window.addEventListener("scroll", onScroll, { passive: true });
    document.addEventListener("visibilitychange", onVis);
    return () => {
      stop();
      io.disconnect();
      ro.disconnect();
      window.removeEventListener("scroll", onScroll);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, []);

  return <canvas ref={ref} aria-hidden="true" className="absolute inset-0 h-full w-full [image-rendering:pixelated]" />;
}

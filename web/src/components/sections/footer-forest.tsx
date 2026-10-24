"use client";

import { useEffect, useRef } from "react";
import { buildForest, CELL, compose, glyphAtlas } from "./forest";

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
    let started = false;

    const shifts = () => {
      if (reduced) return [0, 0];
      const r = canvas.getBoundingClientRect();
      const p = Math.min(1, Math.max(0, (window.innerHeight - r.top) / (window.innerHeight + r.height)));
      return [Math.round((1 - p) * 6), Math.round((1 - p) * 14)];
    };

    const paint = () => {
      raf = 0;
      if (!forest || !atlas) return;
      const [far, near] = shifts();
      compose(forest, far, near, cur);
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
      forest = buildForest(w, h);
      atlas = glyphAtlas(dpr);
      cur = new Uint8Array(forest.cols * forest.rows);
      prev = new Uint8Array(forest.cols * forest.rows).fill(255);
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      paint();
    };

    const onScroll = () => {
      if (!raf) raf = requestAnimationFrame(paint);
    };

    const io = new IntersectionObserver(
      ([e]) => {
        if (!e.isIntersecting) return;
        if (!started) {
          started = true;
          build();
        }
      },
      { rootMargin: "400px 0px" },
    );
    io.observe(canvas);
    let width = 0;
    const ro = new ResizeObserver(([e]) => {
      const w = Math.round(e.contentRect.width);
      if (!started || w === width) return;
      width = w;
      build();
    });
    ro.observe(canvas);
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      cancelAnimationFrame(raf);
      io.disconnect();
      ro.disconnect();
      window.removeEventListener("scroll", onScroll);
    };
  }, []);

  return <canvas ref={ref} aria-hidden="true" className="absolute inset-0 h-full w-full [image-rendering:pixelated]" />;
}

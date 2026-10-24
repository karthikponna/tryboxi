"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import type { MacSceneHandle } from "./mac-three";

const idle = (fn: () => void) => {
  if (typeof window.requestIdleCallback === "function") window.requestIdleCallback(fn, { timeout: 1500 });
  else setTimeout(fn, 300);
};

export function MacScene({ className }: { className?: string }) {
  const host = useRef<HTMLDivElement>(null);
  const handle = useRef<MacSceneHandle | null>(null);
  const [ready, setReady] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    let cancelled = false;
    let started = false;
    const begin = () => {
      if (started) return;
      started = true;
      idle(async () => {
        const css = getComputedStyle(document.documentElement);
        const mono = `${css.getPropertyValue("--font-mono").trim() || "ui-monospace"}, ui-monospace, Menlo, monospace`;
        const sans = `${css.getPropertyValue("--font-sans").trim() || "system-ui"}, system-ui, sans-serif`;
        try {
          await document.fonts.load(`15px ${mono}`);
        } catch {
          // fall back to system fonts
        }
        const { startMacScene } = await import("./mac-three");
        if (cancelled) return;
        try {
          const h = await startMacScene({
            host: el,
            reduced: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
            mono,
            sans,
            onSpawn: (title, count) =>
              setMessage(`Started sandbox ${title}. ${count} microVM${count === 1 ? "" : "s"} running.`),
            onReady: () => setReady(true),
          });
          if (cancelled) h.dispose();
          else handle.current = h;
        } catch {
          // no WebGL: leave the space empty rather than break the page
        }
      });
    };
    const io = new IntersectionObserver(([e]) => {
      if (e.isIntersecting) {
        begin();
        io.disconnect();
      }
    });
    io.observe(el);
    return () => {
      cancelled = true;
      io.disconnect();
      handle.current?.dispose();
      handle.current = null;
    };
  }, []);

  const onKey = useCallback((e: React.KeyboardEvent) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    handle.current?.spawn();
  }, []);

  return (
    <div className={cn("relative", className)}>
      <div
        ref={host}
        role="button"
        tabIndex={0}
        aria-label="iMac and Mac mini running sandbox microVMs. Press Enter to start another microVM."
        onKeyDown={onKey}
        className={cn(
          "absolute inset-0 rounded-[10px] opacity-0 transition-opacity duration-700 ease-(--bx-ease-out) outline-none focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-green",
          ready && "opacity-100",
        )}
      />
      <p className="sr-only" aria-live="polite">
        {message}
      </p>
    </div>
  );
}

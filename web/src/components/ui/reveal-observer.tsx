"use client";

import { useEffect } from "react";

// Hides [data-reveal] / [data-stream] elements that start below the fold and shows them
// on scroll. Server HTML stays fully visible, so nothing depends on this script running.
export function RevealObserver() {
  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    if (!("IntersectionObserver" in window)) return;
    const attr = (el: Element) => (el.hasAttribute("data-stream") ? "data-stream" : "data-reveal");
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          e.target.setAttribute(attr(e.target), "shown");
          io.unobserve(e.target);
        }
      },
      { rootMargin: "0px 0px -8% 0px" },
    );
    const fold = window.innerHeight;
    document.querySelectorAll("[data-reveal],[data-stream]").forEach((el) => {
      if (el.getBoundingClientRect().top <= fold) return;
      el.setAttribute(attr(el), "hidden");
      io.observe(el);
    });
    return () => io.disconnect();
  }, []);
  return null;
}

"use client";

import { useEffect, useState } from "react";

export function HeaderShell({ children }: { children: React.ReactNode }) {
  const [scrolled, setScrolled] = useState(false);
  useEffect(() => {
    const on = () => setScrolled(window.scrollY > 24);
    on();
    window.addEventListener("scroll", on, { passive: true });
    return () => window.removeEventListener("scroll", on);
  }, []);
  return (
    <header
      data-scrolled={scrolled}
      className="fixed inset-x-0 top-0 z-50 h-16 border-b border-transparent transition-[background-color,border-color] duration-200 data-[scrolled=true]:border-line data-[scrolled=true]:bg-bg/80"
    >
      {children}
    </header>
  );
}

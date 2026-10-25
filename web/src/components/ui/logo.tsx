import { cn } from "@/lib/utils";

// Two offset squares drawn as one outline (viewBox 0 0 30 26). Explicit L commands keep
// the path readable by Satori, which renders the favicon and OG images.
export const LOGO_PATH = "M 0 0 L 16 0 L 16 11 L 30 11 L 30 26 L 14 26 L 14 15 L 0 15 Z";

export function LogoGlyph({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 30 26" width="22" height="19" aria-hidden="true" className={cn("shrink-0", className)}>
      <path d={LOGO_PATH} fill="var(--bx-orange)" />
    </svg>
  );
}

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2.5 text-fg", className)}>
      <LogoGlyph />
      <span className="font-display text-[20px] leading-none font-medium tracking-[-0.02em]">tryboxi</span>
    </span>
  );
}

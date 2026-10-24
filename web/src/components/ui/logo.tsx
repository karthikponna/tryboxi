import { cn } from "@/lib/utils";

export function LogoGlyph({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 20 20"
      width="20"
      height="20"
      aria-hidden="true"
      className={cn("shrink-0", className)}
    >
      <path
        d="M13.5 2.75H2.75v14.5h14.5V6.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="square"
      />
      <rect x="15.25" y="0.75" width="4" height="4" fill="var(--bx-green)" />
    </svg>
  );
}

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2 text-fg", className)}>
      <LogoGlyph />
      <span className="font-display text-[20px] leading-none font-medium tracking-[-0.02em]">
        boxi
      </span>
    </span>
  );
}

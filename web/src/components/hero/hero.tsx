import { HeroDither } from "./hero-dither";
import { MacScene } from "./mac-scene";

const FACTS = ["macOS 14+", "Free, runs on your Mac"];

export function Hero() {
  return (
    <section aria-labelledby="hero-title" className="relative z-10 overflow-x-clip bg-black">
      <HeroDither />
      <div className="relative mx-auto grid max-w-[1200px] grid-cols-1 items-center gap-6 px-4 pt-[120px] pb-16 sm:px-6 lg:min-h-[720px] lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] lg:gap-8 lg:pt-[112px] lg:pb-20">
        <div className="w-full max-w-[560px]">
          <h1
            id="hero-title"
            className="font-display text-[40px] leading-[1.02] font-medium tracking-[-0.035em] md:text-[52px] lg:text-[64px]"
          >
            Disposable microVMs for your agents. On your machine.
          </h1>
          <p className="mt-6 max-w-[480px] text-[17px] leading-[1.55] tracking-[-0.01em] text-fg-muted lg:text-[19px]">
            Give Cursor, Claude Code, and Codex a powerful Linux machine for agents to run the code securely.
          </p>
          <div className="mt-9 flex flex-wrap items-center gap-3">
            <a
              href="#get-started"
              className="bx-kiln-panel group inline-flex h-12 items-center gap-2.5 px-5 text-[15px] font-medium text-white transition-[filter] duration-150 hover:brightness-110 active:brightness-95"
            >
              Get started
              <svg
                viewBox="0 0 16 16"
                aria-hidden="true"
                className="size-4 transition-transform duration-200 ease-(--bx-ease-out) group-hover:translate-y-0.5"
                fill="none"
                stroke="currentColor"
                strokeWidth={1.75}
                strokeLinecap="square"
              >
                <path d="M8 2.5v10M3.5 8.5 8 13l4.5-4.5" />
              </svg>
            </a>
            <a
              href="#how-it-works"
              className="inline-flex h-12 items-center border border-white/15 px-5 text-[15px] font-medium text-fg transition-colors duration-150 hover:border-white/25 hover:bg-white/[0.06]"
            >
              How it works
            </a>
          </div>
          <ul className="mt-8 flex flex-wrap gap-x-5 gap-y-2 font-mono text-[13px] text-fg-dim">
            {FACTS.map((f) => (
              <li key={f} className="flex items-center gap-2">
                <span aria-hidden="true" className="size-1.5 bg-orange" />
                {f}
              </li>
            ))}
          </ul>
        </div>
        <MacScene className="aspect-[5/4] w-full lg:aspect-auto lg:h-[560px]" />
      </div>
    </section>
  );
}

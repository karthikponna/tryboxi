import { InstallBox } from "@/components/ui/install-box";
import { BoxField } from "./box-field";
import { StaticFrame } from "./static-frame";

export function Hero() {
  return (
    <section aria-labelledby="hero-title" className="relative overflow-hidden md:min-h-[800px]">
      <div className="pointer-events-none relative z-10 mx-auto max-w-[1200px] px-4 pt-[120px] sm:px-6 md:pt-[152px] md:pb-[128px]">
        <div id="hero-copy" className="pointer-events-auto w-full md:max-w-[440px] lg:max-w-[560px]">
          <h1
            id="hero-title"
            className="font-display text-[40px] leading-[1.02] font-medium tracking-[-0.035em] md:text-[52px] lg:text-[64px]"
          >
            Disposable microVMs for your agents. On your machine.
            <span aria-hidden="true" className="bx-caret ml-[0.06em] font-mono font-normal">
              ▍
            </span>
          </h1>
          <p className="mt-6 max-w-[520px] text-[17px] leading-[1.5] tracking-[-0.01em] text-fg-muted lg:text-[19px]">
            Give Cursor, Claude Code, and your own agents a real Linux machine to run tests, installs, servers, and
            untrusted code. Your files and secrets stay out of reach.
          </p>
          <InstallBox id="install" className="mt-9" />
          <p className="mt-5 font-mono text-[12.5px] leading-[1.5] text-fg-dim">
            Works with Cursor · Claude Code · Codex · any HTTP client
          </p>
        </div>
      </div>
      <BoxField>
        <StaticFrame />
      </BoxField>
    </section>
  );
}

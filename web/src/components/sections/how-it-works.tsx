import { AsciiDiagram } from "@/components/ui/ascii-diagram";
import { BoxFrame } from "@/components/ui/box-frame";
import { Section, SectionHeader } from "@/components/ui/section-header";
import { ARCH } from "./diagram";

const STEPS = [
  { title: "Ask", body: "Your agent calls sandbox_exec over MCP, or your code calls the HTTP API." },
  { title: "Run", body: "sandboxd hands it a microVM in milliseconds and syncs your repo in." },
  { title: "Contain", body: "Traffic goes through an allowlist proxy. Secrets never enter the VM." },
];

export function HowItWorks() {
  return (
    <Section id="how-it-works" labelledBy="how-title">
      <SectionHeader
        id="how-title"
        eyebrow="01 · Architecture"
        title="One daemon on your Mac, one microVM per agent"
        lead="Your agent talks to sandboxd. sandboxd runs every sandbox as its own microVM."
      />
      <div className="mt-12 grid grid-cols-1 items-center gap-10 lg:mt-16 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)] lg:gap-16">
        <div data-reveal>
          <BoxFrame caption="architecture · your mac" command="sandboxctl doctor" status={{ label: "local", tone: "running" }}>
            <div className="px-4 py-6 sm:px-6 sm:py-8">
              <AsciiDiagram lines={ARCH} scale={1.12} className="mx-auto" />
            </div>
            <p className="sr-only">
              Cursor, Claude Code, and Codex connect over MCP to sandbox-mcp, which calls sandboxd over HTTP on a unix
              socket. Your own code can call sandboxd directly. sandboxd runs each sandbox as a microVM and talks to it
              over vsock. Outbound traffic goes through the egress proxy to allowlisted hosts only.
            </p>
          </BoxFrame>
        </div>
        <ol className="grid gap-10" data-reveal>
          {STEPS.map((s, i) => (
            <li key={s.title} className="flex gap-5">
              <span className="font-display text-[40px] leading-none tracking-[-0.03em] text-fg-dim">
                {String(i + 1).padStart(2, "0")}
              </span>
              <div className="pt-1">
                <h3 className="font-display text-[22px] leading-[1.2] tracking-[-0.015em]">{s.title}</h3>
                <p className="mt-1.5 max-w-[360px] text-[16px] leading-[1.55] text-fg-muted">{s.body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </Section>
  );
}

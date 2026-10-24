import { AsciiDiagram } from "@/components/ui/ascii-diagram";
import { BoxFrame } from "@/components/ui/box-frame";
import { Section, SectionHeader } from "@/components/ui/section-header";
import { ARCH } from "./diagram";

const STEPS = [
  {
    title: "Your agent calls sandbox_exec",
    body: "Cursor, Claude Code, and Codex talk MCP over stdio to sandbox-mcp. Your own harness talks HTTP to the same daemon on a unix socket.",
  },
  {
    title: "sandboxd hands it a VM",
    body: "From the warm pool in 20–50 ms, or from a memory snapshot in about half a second. Your repo syncs in, one way, before every command.",
  },
  {
    title: "The command runs on its own kernel",
    body: "sandbox-agent is PID 1 in every microVM and speaks gRPC over vsock. Heartbeats on that link catch a lost VM, and its workspace can be recovered.",
  },
  {
    title: "Traffic leaves through the egress proxy",
    body: "Only allowlisted hosts are reachable. Secrets are swapped in at the proxy, and your LAN and cloud metadata addresses are always blocked.",
  },
];

export function HowItWorks() {
  return (
    <Section id="how-it-works" labelledBy="how-title">
      <SectionHeader
        id="how-title"
        eyebrow="02 · Architecture"
        title="One daemon on your Mac, one microVM per agent"
        lead="sandboxd schedules the VMs, syncs your repo, and owns the network. Agents only ever see a Linux shell."
      />
      <div className="mt-12 grid grid-cols-1 items-start gap-10 lg:mt-16 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:gap-16">
        <div data-reveal>
          <BoxFrame
            caption="architecture · your mac"
            command="sandboxctl doctor"
            status={{ label: "local", tone: "running" }}
          >
            <div className="px-4 py-6 sm:px-6 sm:py-8">
              <AsciiDiagram lines={ARCH} scale={1.12} className="mx-auto" />
            </div>
            <p className="sr-only">
              Cursor, Claude Code, and Codex connect over MCP stdio to sandbox-mcp, which calls sandboxd over HTTP on a
              unix socket. Your own harness can call sandboxd directly with the Go SDK, Python, or curl. sandboxd runs
              each sandbox as a microVM and talks to it with gRPC over vsock, including a heartbeat. Outbound traffic
              from the microVMs goes through the egress proxy to allowlisted hosts only, with secrets swapped in there
              and the LAN blocked.
            </p>
          </BoxFrame>
        </div>
        <ol className="grid gap-8" data-reveal>
          {STEPS.map((s, i) => (
            <li key={s.title} className="grid grid-cols-[32px_1fr] gap-x-3">
              <span className="pt-1.5 font-mono text-[12.5px] text-fg-dim">{String(i + 1).padStart(2, "0")}</span>
              <div>
                <h3 className="font-display text-[22px] leading-[1.2] tracking-[-0.015em]">{s.title}</h3>
                <p className="mt-2 text-[15.5px] leading-[1.6] text-fg-muted">{s.body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </Section>
  );
}

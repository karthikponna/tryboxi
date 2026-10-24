import { Section, SectionHeader } from "@/components/ui/section-header";
import { StatRow } from "@/components/ui/stat-row";
import { TimingBars } from "@/components/ui/timing-bars";

const POINTS = [
  {
    title: "Warm pool",
    body: "sandboxd keeps booted VMs ready and sizes the pool from recent demand, so a new sandbox takes 20–50 ms.",
  },
  {
    title: "Golden snapshots",
    body: "Each template boots once and is saved. New sandboxes restore from that memory snapshot in about half a second.",
  },
  {
    title: "Pause to disk",
    body: "Idle sandboxes pause to disk and resume on the next command in about 0.5 s, so you are not paying for them in RAM.",
  },
];

export function Speed() {
  return (
    <Section id="speed" labelledBy="speed-title">
      <SectionHeader
        id="speed-title"
        eyebrow="03 · Speed"
        title="Fast enough to throw one away"
        lead="Agents start fresh as often as they like. A new sandbox costs milliseconds, not a minute of setup."
      />
      <StatRow
        className="mt-12 lg:mt-16"
        stats={[
          { label: "Warm pool create", value: "20–50 ms" },
          { label: "Snapshot restore", value: "~0.5 s" },
          { label: "Resume from disk", value: "~0.5 s" },
          { label: "Cold boot", value: "~1.2 s" },
          { label: "Cloud bill", value: "$0", className: "col-span-2 lg:col-span-1" },
        ]}
      />
      <p className="mt-4 font-mono text-[12px] leading-[1.5] text-fg-dim">
        Measured on an M4 MacBook with 16 GB. Restores slow to about 0.9 s under memory pressure.
      </p>
      <div className="mt-14 grid grid-cols-1 items-start gap-10 lg:mt-20 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] lg:gap-16">
        <ul className="grid gap-8" data-reveal>
          {POINTS.map((p) => (
            <li key={p.title}>
              <h3 className="font-display text-[22px] leading-[1.2] tracking-[-0.015em]">{p.title}</h3>
              <p className="mt-2 max-w-[460px] text-[15.5px] leading-[1.6] text-fg-muted">{p.body}</p>
            </li>
          ))}
        </ul>
        <div data-reveal>
          <TimingBars
            caption="sandboxctl trace sbx_7f3"
            command="sandboxd serve --otlp-endpoint http://127.0.0.1:4318"
            phases={[
              { name: "scheduler.reserve", ms: 0, label: "0 ms" },
              { name: "vm.restore", ms: 800, label: "~0.8 s" },
              { name: "guest.configure", ms: 2, label: "~2 ms" },
              { name: "workspace.sync", ms: 60, label: "~60 ms" },
              { name: "exec", ms: 23, label: "~23 ms", highlight: true },
            ]}
          />
          <p className="mt-4 text-[14px] leading-[1.6] text-fg-dim">
            One real create-and-exec trace. Spans also export over OpenTelemetry to Jaeger, Grafana Tempo, or any
            collector, next to Prometheus metrics and a Grafana dashboard.
          </p>
        </div>
      </div>
    </Section>
  );
}

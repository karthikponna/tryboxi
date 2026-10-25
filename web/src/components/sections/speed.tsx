import { Accent, Section, SectionHeader } from "@/components/ui/section-header";
import { StatRow } from "@/components/ui/stat-row";

export function Speed() {
  return (
    <Section id="speed" labelledBy="speed-title">
      <SectionHeader
        id="speed-title"
        title={
          <>
            Fast enough to <Accent>throw one away</Accent>
          </>
        }
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
      <p className="mt-4 font-mono text-[12px] leading-[1.5] text-fg-dim">Measured on an M4 MacBook with 16 GB.</p>
    </Section>
  );
}

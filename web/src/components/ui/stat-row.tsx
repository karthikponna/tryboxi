import { cn } from "@/lib/utils";

export type Stat = { label: string; value: string; note?: string; className?: string };

export function StatRow({ stats, className }: { stats: Stat[]; className?: string }) {
  return (
    <dl
      data-reveal
      className={cn("grid grid-cols-2 gap-px border-y border-line bg-line md:grid-cols-3 lg:grid-cols-5", className)}
    >
      {stats.map((s) => (
        <div key={s.label} className={cn("bg-bg px-4 py-7 sm:px-6 lg:py-8", s.className)}>
          <dt className="font-mono text-[11.5px] leading-[1.3] tracking-[0.12em] text-fg-dim uppercase">{s.label}</dt>
          <dd className="mt-3 font-display text-[34px] leading-none tracking-[-0.03em] whitespace-nowrap text-fg lg:text-[40px]">
            {s.value}
          </dd>
          {s.note && <dd className="mt-2 font-mono text-[12px] leading-[1.4] text-fg-dim">{s.note}</dd>}
        </div>
      ))}
    </dl>
  );
}

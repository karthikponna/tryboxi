import { EmberRule } from "@/components/ui/ember-rule";
import { cn } from "@/lib/utils";

export function Accent({ children }: { children: React.ReactNode }) {
  return <span className="text-orange">{children}</span>;
}

export function SectionHeader({
  id,
  title,
  lead,
  className,
}: {
  id: string;
  title: React.ReactNode;
  lead?: React.ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("max-w-[640px]", className)} data-reveal>
      <h2 id={id} className="font-display text-[32px] leading-[1.06] tracking-[-0.03em] lg:text-[44px]">
        {title}
      </h2>
      {lead && (
        <p className="mt-4 text-[17px] leading-[1.5] tracking-[-0.01em] text-fg-muted lg:text-[19px]">
          {lead}
        </p>
      )}
    </header>
  );
}

export function Section({
  id,
  labelledBy,
  className,
  children,
}: {
  id: string;
  labelledBy: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={labelledBy}
      className={cn("bx-section relative border-t border-line py-[72px] lg:py-[120px]", className)}
    >
      <EmberRule />
      <div className="relative mx-auto max-w-[1200px] px-4 sm:px-6">{children}</div>
    </section>
  );
}

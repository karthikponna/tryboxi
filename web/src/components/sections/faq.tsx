import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Section, SectionHeader } from "@/components/ui/section-header";

const code = "font-mono text-[0.9em] text-fg";

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "What do I need to run it?",
    a: (
      <>
        A Mac with Apple Silicon on macOS 14 or later. VMs run on Apple&apos;s Virtualization.framework inside{" "}
        <span className={code}>sandboxd</span>, with no root, Lima, or Docker needed at runtime. For now you build it
        from source with Go; building the base Ubuntu image uses Docker once.
      </>
    ),
  },
  {
    q: "How many sandboxes fit on my Mac?",
    a: (
      <>
        By default <span className={code}>sandboxd</span> budgets half your RAM and gives each sandbox 2 GB, so a 16 GB
        Mac runs four at once. Change <span className={code}>budgets.max_mem_mb</span> in{" "}
        <span className={code}>config.yaml</span> to raise or lower the budget.
      </>
    ),
  },
  {
    q: "Is it free?",
    a: "Yes. boxi is open source and runs on your own hardware, so there is no cloud bill.",
  },
  {
    q: "Does my code leave my machine?",
    a: "No. Sandboxes run on your Mac, files move in and out only by explicit copy over vsock, and network access is allowlist-only. Stored secrets never enter a VM at all.",
  },
  {
    q: "What about Linux and Windows?",
    a: "Linux support is coming soon. Its Firecracker backend compiles and is unit-tested, and testing on real KVM hosts is next. Windows is not supported yet.",
  },
];

export function Faq() {
  return (
    <Section id="faq" labelledBy="faq-title">
      <div className="grid grid-cols-1 gap-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] lg:gap-16">
        <SectionHeader id="faq-title" eyebrow="08 · FAQ" title="Questions" lead="Short answers. The README has the rest." />
        <div data-reveal>
          <Accordion type="single" collapsible defaultValue="q0">
            {FAQ.map((f, i) => (
              <AccordionItem key={f.q} value={`q${i}`}>
                <AccordionTrigger>{f.q}</AccordionTrigger>
                <AccordionContent>{f.a}</AccordionContent>
              </AccordionItem>
            ))}
          </Accordion>
        </div>
      </div>
    </Section>
  );
}

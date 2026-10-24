import { Hero } from "@/components/hero/hero";
import { Comparison } from "@/components/sections/comparison";
import { Connect } from "@/components/sections/connect";
import { Cta } from "@/components/sections/cta";
import { Faq } from "@/components/sections/faq";
import { Features } from "@/components/sections/features";
import { HowItWorks } from "@/components/sections/how-it-works";
import { Safety } from "@/components/sections/safety";
import { SiteFooter } from "@/components/sections/site-footer";
import { SiteHeader } from "@/components/sections/site-header";
import { Speed } from "@/components/sections/speed";
import { UseCase } from "@/components/sections/use-case";
import { RevealObserver } from "@/components/ui/reveal-observer";

export default function Home() {
  return (
    <>
      <a
        href="#main"
        className="sr-only z-[60] rounded-[6px] bg-panel-2 px-3 py-2 font-mono text-[12.5px] text-fg focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >
        Skip to content
      </a>
      <SiteHeader />
      <main id="main" tabIndex={-1} className="outline-none">
        <Hero />
        <Features />
        <HowItWorks />
        <Speed />
        <Safety />
        <Connect />
        <UseCase />
        <Comparison />
        <Faq />
        <Cta />
      </main>
      <SiteFooter />
      <RevealObserver />
    </>
  );
}

import { Hero } from "@/components/hero/hero";
import { Connect } from "@/components/sections/connect";
import { Faq } from "@/components/sections/faq";
import { HowItWorks } from "@/components/sections/how-it-works";
import { SiteFooter } from "@/components/sections/site-footer";
import { SiteHeader } from "@/components/sections/site-header";
import { Speed } from "@/components/sections/speed";
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
        <HowItWorks />
        <Speed />
        <Connect />
        <Faq />
      </main>
      <SiteFooter />
      <RevealObserver />
    </>
  );
}

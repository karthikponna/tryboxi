import { InstallBox } from "@/components/ui/install-box";
import { HeroDither } from "./hero-dither";
import { MacScene } from "./mac-scene";

export function Hero() {
  return (
    <section aria-labelledby="hero-title" className="relative overflow-hidden bg-black">
      <HeroDither />
      <div className="relative mx-auto grid max-w-[1200px] grid-cols-1 items-center gap-6 px-4 pt-[120px] pb-16 sm:px-6 lg:min-h-[720px] lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] lg:gap-8 lg:pt-[112px] lg:pb-20">
        <div className="w-full max-w-[560px]">
          <h1
            id="hero-title"
            className="font-display text-[40px] leading-[1.02] font-medium tracking-[-0.035em] md:text-[52px] lg:text-[64px]"
          >
            Disposable microVMs for your agents. On your machine.
          </h1>
          <InstallBox id="install" className="mt-10" />
        </div>
        <MacScene className="aspect-[5/4] w-full lg:aspect-auto lg:h-[560px]" />
      </div>
    </section>
  );
}

import { GitHubIcon, XIcon } from "@/components/ui/brand-icons";
import { site } from "@/site.config";
import { FooterForest } from "./footer-forest";

const link =
  "rounded-[6px] p-1 text-[rgb(226_150_105/0.7)] transition-colors duration-150 hover:text-[#f0a070] focus-visible:outline-[#e27a38]";

export function SiteFooter() {
  return (
    <footer className="relative h-[480px] overflow-hidden border-t border-line bg-[#070504] sm:h-[560px] lg:h-[640px]">
      <FooterForest />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 bg-[repeating-linear-gradient(to_bottom,rgb(0_0_0/0.28)_0_1px,transparent_1px_3px)]"
      />
      <div className="relative mx-auto max-w-[1200px] px-4 pt-8 sm:px-6">
        <nav aria-label="Social" className="-ml-1 flex items-center gap-3">
          <a href={site.repo.url} aria-label="tryboxi on GitHub" className={link}>
            <GitHubIcon className="size-5" />
          </a>
          <a href={site.social.x} aria-label="Karthik Ponna on X" className={link}>
            <XIcon className="size-[18px]" />
          </a>
        </nav>
      </div>
    </footer>
  );
}

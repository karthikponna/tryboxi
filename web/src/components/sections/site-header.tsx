import Link from "next/link";
import { GitHubIcon } from "@/components/ui/brand-icons";
import { Logo } from "@/components/ui/logo";
import { site } from "@/site.config";
import { HeaderShell } from "./header-shell";

export function SiteHeader() {
  return (
    <HeaderShell>
      <div className="mx-auto flex h-16 max-w-[1200px] items-center justify-between gap-3 px-4 sm:px-6">
        <Link href="/" aria-label="boxi home" className="-m-1 rounded-[6px] p-1">
          <Logo />
        </Link>
        <a
          href={site.repo.url}
          aria-label="boxi on GitHub"
          className="-m-1.5 rounded-full p-1.5 text-fg-muted transition-colors duration-150 hover:text-fg"
        >
          <GitHubIcon className="size-6" />
        </a>
      </div>
    </HeaderShell>
  );
}

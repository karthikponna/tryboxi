import { Suspense } from "react";
import Link from "next/link";
import { GitHubIcon, StarIcon } from "@/components/ui/brand-icons";
import { Logo } from "@/components/ui/logo";
import { formatStars, getStarCount } from "@/lib/github";
import { site } from "@/site.config";
import { HeaderShell } from "./header-shell";

function StarButton({ stars }: { stars: number | null }) {
  return (
    <a
      href={site.repo.url}
      aria-label={stars === null ? "Star tryboxi on GitHub" : `Star tryboxi on GitHub, ${stars} stars`}
      className="group inline-flex h-9 items-center gap-2 rounded-[10px] border border-white/10 bg-white/[0.06] px-3 text-[14px] font-medium text-fg transition-colors duration-150 hover:border-white/15 hover:bg-white/[0.1]"
    >
      <GitHubIcon className="size-[18px]" />
      <span>Star</span>
      <StarIcon className="size-4 fill-transparent transition-[fill,scale] duration-200 ease-(--bx-ease-out) group-hover:scale-110 group-hover:fill-current" />
      {stars !== null && <span className="tabular-nums">{formatStars(stars)}</span>}
    </a>
  );
}

async function LiveStarButton() {
  return <StarButton stars={await getStarCount()} />;
}

export function SiteHeader() {
  return (
    <HeaderShell>
      <div className="mx-auto flex h-16 max-w-[1200px] items-center justify-between gap-3 px-4 sm:px-6">
        <Link href="/" aria-label="tryboxi home" className="-m-1 rounded-[6px] p-1">
          <Logo />
        </Link>
        <Suspense fallback={<StarButton stars={null} />}>
          <LiveStarButton />
        </Suspense>
      </div>
    </HeaderShell>
  );
}

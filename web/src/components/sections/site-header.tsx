import Link from "next/link";
import { Arrow, Button, buttonVariants } from "@/components/ui/button";
import { Logo } from "@/components/ui/logo";
import { formatStars, getStars } from "@/lib/github";
import { cn } from "@/lib/utils";
import { site } from "@/site.config";
import { HeaderShell } from "./header-shell";

async function GitHubStars() {
  const stars = await getStars();
  if (stars === null) return null;
  return (
    <a
      href={site.repo.url}
      className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "gap-1.5 px-2.5 sm:px-3")}
    >
      GitHub
      {stars > 0 && (
        <span className="font-mono text-[12.5px] text-fg-dim">
          <span aria-hidden="true">★ </span>
          {formatStars(stars)}
          <span className="sr-only"> stars</span>
        </span>
      )}
    </a>
  );
}

export function SiteHeader() {
  return (
    <HeaderShell>
      <div className="mx-auto flex h-16 max-w-[1200px] items-center justify-between gap-3 px-4 sm:px-6">
        <Link href="/" aria-label="boxi home" className="-m-1 rounded-[6px] p-1">
          <Logo />
        </Link>
        <nav aria-label="Primary" className="flex items-center gap-0.5 sm:gap-2">
          <GitHubStars />
          <a href={site.links.docs} className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "px-2.5 sm:px-3")}>
            Docs
          </a>
          <Button asChild className="ml-1.5 sm:ml-1">
            <a href="#install">
              Install <Arrow />
            </a>
          </Button>
        </nav>
      </div>
    </HeaderShell>
  );
}

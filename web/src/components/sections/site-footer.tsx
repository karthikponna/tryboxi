import { BoxFrame } from "@/components/ui/box-frame";
import { Logo } from "@/components/ui/logo";
import { site } from "@/site.config";

const COLUMNS: { title: string; links: { label: string; href: string }[] }[] = [
  {
    title: "Product",
    links: [
      { label: "Isolation", href: "#features" },
      { label: "Architecture", href: "#how-it-works" },
      { label: "Speed", href: "#speed" },
      { label: "Safety model", href: "#safety" },
      { label: "FAQ", href: "#faq" },
    ],
  },
  {
    title: "Developers",
    links: [
      { label: "README", href: site.links.docs },
      { label: "Testing guide", href: site.links.testing },
      { label: "HTTP API", href: site.links.openapi },
      { label: "Go SDK", href: site.links.goSdk },
      { label: "Examples", href: site.links.examples },
    ],
  },
  {
    title: "Project",
    links: [
      { label: "GitHub", href: site.repo.url },
      { label: "Issues", href: site.links.issues },
      { label: "Agent rules", href: site.links.agentRules },
      { label: "Grafana dashboard", href: site.links.grafana },
    ],
  },
];

export function SiteFooter() {
  return (
    <footer className="border-t border-line">
      <div className="mx-auto grid max-w-[1200px] grid-cols-1 gap-12 px-4 py-16 sm:px-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)] lg:py-20">
        <BoxFrame
          caption="boxi · local"
          command="sandboxd version"
          caret
          status={{ label: "your mac", tone: "running" }}
          className="max-w-[360px]"
        >
          <div className="px-4 py-5">
            <Logo />
            <p className="mt-3 text-[14px] leading-[1.6] text-fg-muted">Disposable microVMs for your agents.</p>
          </div>
        </BoxFrame>
        <nav aria-label="Footer" className="grid grid-cols-2 gap-8 sm:grid-cols-3">
          {COLUMNS.map((col) => (
            <div key={col.title}>
              <h2 className="font-mono text-[11.5px] leading-none font-medium tracking-[0.12em] text-fg-dim uppercase">
                {col.title}
              </h2>
              <ul className="mt-5 grid gap-1">
                {col.links.map((l) => (
                  <li key={l.label}>
                    <a
                      href={l.href}
                      className="inline-block py-1 font-mono text-[13px] leading-[1.5] text-fg-muted transition-colors duration-150 hover:text-fg"
                    >
                      {l.label}
                    </a>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>
      </div>
      <div className="border-t border-line">
        <div className="mx-auto flex max-w-[1200px] flex-col gap-2 px-4 py-6 font-mono text-[12px] leading-[1.5] text-fg-dim sm:flex-row sm:items-center sm:justify-between sm:px-6">
          <p>Open source · runs on your machine · $0 cloud bill</p>
          <p>{site.url.replace("https://", "")}</p>
        </div>
      </div>
    </footer>
  );
}

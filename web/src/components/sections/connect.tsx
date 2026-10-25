import { CodeTabs } from "@/components/ui/code-tabs";
import { InstallBox } from "@/components/ui/install-box";
import { Accent, Section, SectionHeader } from "@/components/ui/section-header";
import { SNIPPETS } from "./connect-snippets";

const code = "font-mono text-[0.9em] whitespace-nowrap text-orange-hover";

function Step({
  n,
  title,
  children,
  panel,
}: {
  n: number;
  title: string;
  children: React.ReactNode;
  panel: React.ReactNode;
}) {
  return (
    <li
      className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)] lg:gap-12"
      data-reveal
    >
      <div className="lg:pt-1">
        <div className="flex items-center gap-3">
          <span
            aria-hidden="true"
            className="bx-kiln-panel inline-flex size-8 shrink-0 items-center justify-center font-mono text-[14px] text-white"
          >
            {n}
          </span>
          <h3 className="font-display text-[22px] leading-[1.15] tracking-[-0.02em] lg:text-[24px]">
            <span className="sr-only">Step {n}: </span>
            {title}
          </h3>
        </div>
        <p className="mt-4 text-[16px] leading-[1.6] text-fg-muted">{children}</p>
      </div>
      <div className="min-w-0">{panel}</div>
    </li>
  );
}

export function Connect() {
  return (
    <Section id="get-started" labelledBy="connect-title">
      <SectionHeader
        id="connect-title"
        title={
          <>
            Install once, then <Accent>connect your agent</Accent>
          </>
        }
        lead="Two steps in a terminal on your Mac. Both run from the folder you clone."
      />
      <ol className="mt-12 grid grid-cols-1 gap-14 lg:mt-16 lg:gap-20">
        <Step n={1} title="Install tryboxi" panel={<InstallBox id="install" />}>
          Builds from source, so you need Go, plus Docker once to build the Ubuntu base image. The first build takes a
          few minutes. <span className={code}>sandboxd install</span> registers the daemon to start at login, and from
          then on it needs no Docker or root.
        </Step>
        <Step n={2} title="Connect your agent" panel={<CodeTabs snippets={SNIPPETS} />}>
          Point <span className={code}>--project</span> at the repo your agent works in. It writes the MCP config and
          sandbox rules there. Building your own harness? Call the HTTP API on the unix socket instead.
        </Step>
      </ol>
    </Section>
  );
}

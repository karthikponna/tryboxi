"use client";

import { site, type InstallOs } from "@/site.config";
import { BoxCorners } from "@/components/ui/box-frame";
import { CopyButton, useCopy } from "@/components/ui/copy-button";
import { StatusChip } from "@/components/ui/status-chip";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

function Command({ text }: { text: string }) {
  const parts = text.split(/(<[^>]+>)/g);
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith("<") ? (
          <span key={i} className="text-fg-muted italic">
            {p}
          </span>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  );
}

function CommandRows({ lines, os }: { lines: readonly string[]; os: string }) {
  const text = lines.join("\n");
  const { copied, copy } = useCopy(text);
  return (
    <div className="flex cursor-copy items-start gap-3 py-3 pr-3 pl-4" onClick={copy}>
      <div
        tabIndex={0}
        role="region"
        aria-label={`Install commands for ${os}`}
        className="bx-scroll bx-fade-right min-w-0 flex-1 overflow-x-auto rounded-[4px] focus-visible:outline-offset-4"
      >
        <pre className="w-max pr-8 font-mono text-[12.5px] leading-[1.75] text-fg">
          {lines.map((line) => (
            <div key={line}>
              <span className="text-fg-dim select-none">$ </span>
              <Command text={line} />
            </div>
          ))}
        </pre>
      </div>
      <CopyButton copied={copied} onCopy={copy} label="install commands" />
    </div>
  );
}

function Soon({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-start gap-2.5 px-4 py-3.5 sm:flex-row sm:items-center sm:gap-3">
      <StatusChip status="stopped">{label}</StatusChip>
      <p className="text-[13.5px] leading-[1.5] text-fg-muted">{children}</p>
    </div>
  );
}

const chip = "rounded-full bg-panel-2 px-1.5 py-[3px] text-[10.5px] leading-none text-fg-dim";

const OSES: {
  value: InstallOs;
  label: string;
  chip?: string;
  state?: { label: string; text: string };
}[] = [
  { value: "macos", label: "macOS" },
  {
    value: "linux",
    label: "Linux",
    chip: "soon",
    state: {
      label: "coming soon",
      text: "The Linux backend runs on Firecracker. It compiles and is unit-tested; real KVM testing is next.",
    },
  },
  {
    value: "windows",
    label: "Windows",
    chip: "not yet",
    state: {
      label: "not supported yet",
      text: "Windows hosts have no backend yet (it would need Hyper-V or WSL2).",
    },
  },
];

export function InstallBox({ id, className }: { id?: string; className?: string }) {
  return (
    <div
      id={id}
      className={cn("@container relative w-full max-w-[560px] rounded-[10px] border border-line bg-panel", className)}
    >
      <BoxCorners />
      <Tabs defaultValue="macos">
        <div className="flex items-center justify-between border-b border-line">
          <TabsList aria-label="Choose your operating system">
            {OSES.map((os, i) => (
              <TabsTrigger key={os.value} value={os.value} className={i === 0 ? "rounded-tl-[9px]" : undefined}>
                {os.label}
                {os.chip && site.install[os.value] === null && <span className={chip}>{os.chip}</span>}
              </TabsTrigger>
            ))}
          </TabsList>
          <span className="hidden pr-4 font-mono text-[11.5px] whitespace-nowrap text-fg-dim @[520px]:block">
            Apple Silicon · macOS 14+
          </span>
        </div>
        {OSES.map((os) => {
          const lines = site.install[os.value];
          return (
            <TabsContent key={os.value} value={os.value} tabIndex={lines ? -1 : 0}>
              {lines ? (
                <CommandRows lines={lines} os={os.label} />
              ) : (
                os.state && <Soon label={os.state.label}>{os.state.text}</Soon>
              )}
            </TabsContent>
          );
        })}
      </Tabs>
    </div>
  );
}

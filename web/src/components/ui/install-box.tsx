"use client";

import { site } from "@/site.config";
import { BoxCorners } from "@/components/ui/box-frame";
import { CopyButton, useCopy } from "@/components/ui/copy-button";
import { cn } from "@/lib/utils";

function Command({ text }: { text: string }) {
  return (
    <>
      {text.split(/(<[^>]+>)/g).map((p, i) =>
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

export function InstallBox({ id, className }: { id?: string; className?: string }) {
  const lines = site.install.macos;
  const { copied, copy } = useCopy(lines.join("\n"));
  return (
    <div
      id={id}
      className={cn("relative w-full max-w-[560px] rounded-[10px] border border-line bg-panel", className)}
    >
      <BoxCorners />
      <div className="flex border-b border-line">
        <p className="relative flex h-9 items-center rounded-tl-[9px] bg-green-tint px-3.5 font-mono text-[12px] leading-none text-fg after:absolute after:inset-x-0 after:bottom-0 after:h-px after:bg-green">
          macOS
        </p>
      </div>
      <div className="flex cursor-copy items-start gap-3 py-3 pr-3 pl-4" onClick={copy}>
        <div
          tabIndex={0}
          role="region"
          aria-label="Install commands for macOS"
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
    </div>
  );
}

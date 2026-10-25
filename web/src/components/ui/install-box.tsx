"use client";

import { site } from "@/site.config";
import { CopyButton, useCopy } from "@/components/ui/copy-button";
import { cn } from "@/lib/utils";

function Command({ text }: { text: string }) {
  return (
    <>
      {text.split(/(<[^>]+>)/g).map((p, i) =>
        p.startsWith("<") ? (
          <span key={i} className="text-kiln-muted italic">
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
    <div id={id} className={cn("bx-kiln-panel w-full p-5 sm:p-6", className)}>
      <p className="inline-flex h-8 items-center bg-kiln-dark px-3 font-mono text-[14px] leading-none text-white">
        macOS
      </p>
      <div className="flex items-start gap-3 bg-kiln-dark py-3 pr-3 pl-4">
        <div
          tabIndex={0}
          role="region"
          aria-label="Install commands for macOS"
          className="bx-scroll bx-fade-right min-w-0 flex-1 overflow-x-auto focus-visible:outline-offset-4"
        >
          <pre className="w-max pr-8 font-mono text-[14px] leading-[1.75] text-white">
            {lines.map((line) => (
              <div key={line}>
                <span className="mr-3 text-kiln-muted select-none">$</span>
                <Command text={line} />
              </div>
            ))}
          </pre>
        </div>
        <CopyButton copied={copied} onCopy={copy} label="install commands" className="mt-0.5" />
      </div>
    </div>
  );
}

"use client";

import type { Snippet } from "@/components/sections/connect-snippets";
import { BoxFrame } from "@/components/ui/box-frame";
import { CommandLine } from "@/components/ui/command-line";
import { CopyButton, useCopy } from "@/components/ui/copy-button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

function Panel({ s }: { s: Snippet }) {
  const { copied, copy } = useCopy(s.copy);
  return (
    <BoxFrame caption={s.caption} command={s.footer} status={{ label: "local", tone: "running" }}>
      <CopyButton copied={copied} onCopy={copy} label={s.copyLabel} className="absolute top-3 right-3 z-10 bg-panel" />
      <div
        tabIndex={0}
        role="region"
        aria-label={`${s.label} example`}
        className="bx-scroll overflow-x-auto focus-visible:-outline-offset-2"
      >
        {s.kind === "shell" ? (
          <CommandLine lines={s.lines} className="w-max min-w-full py-4 pr-24" />
        ) : (
          <pre className="w-max min-w-full px-4 py-4 pr-24 font-mono text-[12.5px] leading-[1.6] text-fg [tab-size:4]">
            <code>{s.code}</code>
          </pre>
        )}
      </div>
    </BoxFrame>
  );
}

export function CodeTabs({ snippets }: { snippets: Snippet[] }) {
  return (
    <Tabs defaultValue={snippets[0].value} className="gap-4">
      <TabsList
        aria-label="Choose how your agent connects"
        className="w-full rounded-[10px] border border-line bg-panel sm:w-fit"
      >
        {snippets.map((s, i) => (
          <TabsTrigger
            key={s.value}
            value={s.value}
            className={
              i === 0 ? "rounded-l-[9px]" : i === snippets.length - 1 ? "rounded-r-[9px]" : undefined
            }
          >
            {s.label}
          </TabsTrigger>
        ))}
      </TabsList>
      {snippets.map((s) => (
        <TabsContent key={s.value} value={s.value} tabIndex={-1}>
          <Panel s={s} />
        </TabsContent>
      ))}
    </Tabs>
  );
}

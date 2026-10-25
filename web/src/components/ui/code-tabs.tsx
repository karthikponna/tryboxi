"use client";

import { Fragment } from "react";
import type { Snippet } from "@/components/sections/connect-snippets";
import { CommandLine } from "@/components/ui/command-line";
import { CopyButton, useCopy } from "@/components/ui/copy-button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

function Panel({ s }: { s: Snippet }) {
  const { copied, copy } = useCopy(s.copy);
  return (
    <div className="relative bg-kiln-dark">
      <CopyButton copied={copied} onCopy={copy} label={s.copyLabel} className="absolute top-3 right-4 z-10" />
      <div
        tabIndex={0}
        role="region"
        aria-label={`${s.label} example`}
        className="bx-scroll overflow-x-auto focus-visible:-outline-offset-2"
      >
        {s.kind === "shell" ? (
          <CommandLine lines={s.lines} className="w-max min-w-full px-4 py-3 pr-24" />
        ) : (
          <pre className="w-max min-w-full px-4 py-3 pr-24 font-mono text-[14px] leading-[1.7] text-kiln-text [tab-size:4]">
            <code>{s.code}</code>
          </pre>
        )}
      </div>
    </div>
  );
}

export function CodeTabs({ snippets }: { snippets: Snippet[] }) {
  return (
    <Tabs defaultValue={snippets[0].value} className="bx-kiln-panel p-5 sm:p-6">
      <TabsList aria-label="Choose how your agent connects">
        {snippets.map((s, i) => (
          <Fragment key={s.value}>
            {i > 0 && s.group !== snippets[i - 1].group && (
              <span aria-hidden="true" className="mx-2 w-px shrink-0 self-center h-4 bg-kiln-text/30" />
            )}
            <TabsTrigger value={s.value}>{s.label}</TabsTrigger>
          </Fragment>
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

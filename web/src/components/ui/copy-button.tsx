"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

export function useCopy(text: string) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const copy = useCallback(async () => {
    if (!(await copyText(text))) return;
    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 1600);
  }, [text]);
  return { copied, copy };
}

export function CopyButton({
  copied,
  onCopy,
  label,
  className,
}: {
  copied: boolean;
  onCopy: () => void;
  label: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        onCopy();
      }}
      aria-label={copied ? `${label} copied` : `Copy ${label}`}
      className={cn(
        "inline-flex h-6 shrink-0 items-center px-1 font-mono text-[14px] leading-none transition-colors duration-150",
        copied ? "text-kiln-text" : "text-kiln-muted hover:text-kiln-text",
        className,
      )}
    >
      <span aria-hidden="true">{copied ? "copied ✓" : "copy"}</span>
      <span className="sr-only" aria-live="polite">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </button>
  );
}

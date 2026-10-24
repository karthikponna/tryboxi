import { cn } from "@/lib/utils";

type Status = "running" | "paused" | "stopped" | "blocked";

const dot: Record<Status, string> = {
  running: "bg-green",
  paused: "bg-amber",
  stopped: "bg-fg-dim",
  blocked: "bg-red",
};

type BoxFrameProps = {
  caption: string;
  command?: string;
  caret?: boolean;
  status?: { label: string; tone: Status };
  className?: string;
  bodyClassName?: string;
  children: React.ReactNode;
};

const corner =
  "pointer-events-none absolute font-mono text-[13px] leading-none text-line-strong select-none";

export function BoxCorners() {
  return (
    <>
      <span aria-hidden="true" className={cn(corner, "-top-[7px] -left-[5px]")}>┌</span>
      <span aria-hidden="true" className={cn(corner, "-top-[7px] -right-[5px]")}>┐</span>
      <span aria-hidden="true" className={cn(corner, "-bottom-[6px] -left-[5px]")}>└</span>
      <span aria-hidden="true" className={cn(corner, "-right-[5px] -bottom-[6px]")}>┘</span>
    </>
  );
}

export function BoxFrame({
  caption,
  command,
  caret,
  status = { label: "live", tone: "running" },
  className,
  bodyClassName,
  children,
}: BoxFrameProps) {
  return (
    <figure className={cn("relative min-w-0 rounded-[10px] border border-line bg-panel", className)}>
      <BoxCorners />
      <figcaption className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5 font-mono text-[12.5px] leading-[1.4] text-fg-dim">
        <span className="truncate">{caption}</span>
        <span className="flex shrink-0 items-center gap-1.5">
          <span aria-hidden="true" className={cn("size-1.5 rounded-full", dot[status.tone])} />
          {status.label}
        </span>
      </figcaption>
      <div className={cn("relative overflow-hidden", bodyClassName)}>{children}</div>
      {command && (
        <div className="bx-scroll overflow-x-auto border-t border-line px-4 py-2.5 font-mono text-[12.5px] leading-[1.4] whitespace-nowrap">
          <span className="text-fg-dim">$ </span>
          <span className="text-fg">{command}</span>
          {caret && (
            <span aria-hidden="true" className="bx-caret ml-0.5">
              ▍
            </span>
          )}
        </div>
      )}
    </figure>
  );
}

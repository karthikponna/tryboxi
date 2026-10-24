import { cn } from "@/lib/utils";

export type Line = { kind: "cmd" | "out" | "ok" | "err"; text: string };

function Row({ line }: { line: Line }) {
  switch (line.kind) {
    case "cmd":
      return (
        <>
          <span className="text-fg-dim select-none">$ </span>
          <span className="text-fg">{line.text}</span>
        </>
      );
    case "ok":
      return (
        <>
          <span className="text-green">✓ </span>
          <span className="text-fg">{line.text}</span>
        </>
      );
    case "err":
      return (
        <>
          <span className="text-red">✗ </span>
          <span className="text-fg">{line.text}</span>
        </>
      );
    default:
      return <span className="text-fg-muted">{line.text}</span>;
  }
}

export function CommandLine({ lines, className }: { lines: Line[]; className?: string }) {
  return (
    <div data-stream className={cn("px-4 py-3 font-mono text-[12.5px] leading-[1.6]", className)}>
      {lines.map((line, i) => (
        <div key={i} className="bx-line whitespace-pre" style={{ "--i": i } as React.CSSProperties}>
          <Row line={line} />
        </div>
      ))}
    </div>
  );
}

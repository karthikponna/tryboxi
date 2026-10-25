import { cn } from "@/lib/utils";

export type Line = { kind: "cmd" | "out" | "ok" | "err"; text: string };

function Row({ line }: { line: Line }) {
  switch (line.kind) {
    case "cmd":
      return (
        <>
          <span className="mr-3 text-kiln-muted select-none">$</span>
          <span className="text-white">{line.text}</span>
        </>
      );
    case "ok":
      return (
        <>
          <span className="text-kiln-muted">✓ </span>
          <span className="text-white">{line.text}</span>
        </>
      );
    case "err":
      return (
        <>
          <span className="text-red">✗ </span>
          <span className="text-white">{line.text}</span>
        </>
      );
    default:
      return <span className="text-kiln-text/85">{line.text}</span>;
  }
}

export function CommandLine({ lines, className }: { lines: Line[]; className?: string }) {
  return (
    <div data-stream className={cn("font-mono text-[14px] leading-[1.75]", className)}>
      {lines.map((line, i) => (
        <div key={i} className="bx-line whitespace-pre" style={{ "--i": i } as React.CSSProperties}>
          <Row line={line} />
        </div>
      ))}
    </div>
  );
}

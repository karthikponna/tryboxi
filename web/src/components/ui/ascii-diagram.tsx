import { BASELINE, BOX, CH, CW, type Prim } from "@/components/ui/box-glyphs";
import { cn } from "@/lib/utils";

const R = (x: number, y: number, w: number, h: number): Prim => [0, x, y, w, h];

// Arrowheads are polygons in cell units.
const TRIANGLES: Record<number, { stub: Prim; points: string }> = {
  0x25bc: { stub: R(3, 0, 1, 5), points: "0.75,4.5 6.25,4.5 3.5,10.5" }, // ▼
  0x25b6: { stub: R(0, 6.5, 2, 1), points: "1.5,3.5 6.5,7 1.5,10.5" }, // ▶
};

const LINE = "rgba(226,122,56,0.75)";
const NAME = "#f8f1ea";
const LABEL = "rgba(248,241,234,0.66)";
const NAMES = /(sandbox-mcp|sandboxd|sbx_[0-9a-f]{3}|egress proxy|mac mini at home)/g;

function colors(line: string) {
  const out: string[] = [...line].map(() => LABEL);
  for (const m of line.matchAll(NAMES)) for (let i = 0; i < m[0].length; i++) out[m.index + i] = NAME;
  return out;
}

export function AsciiDiagram({ lines, scale = 1.07, className }: { lines: string[]; scale?: number; className?: string }) {
  const cols = Math.max(...lines.map((l) => [...l].length));
  const w = cols * CW;
  const h = lines.length * CH;
  const paths = new Map<string, string>();
  const tris: React.ReactNode[] = [];
  const texts: React.ReactNode[] = [];
  const addRect = (fill: string, x: number, y: number, p: Prim) => {
    if (p[0] !== 0) return;
    paths.set(fill, (paths.get(fill) ?? "") + `M${x + p[1]} ${y + p[2]}h${p[3]}v${p[4]}h${-p[3]}z`);
  };

  lines.forEach((line, r) => {
    const chars = [...line];
    const tone = colors(line);
    const y = r * CH;
    let xs: number[] = [];
    let str = "";
    let fill = "";
    const flush = () => {
      if (!str.trim()) {
        xs = [];
        str = "";
        return;
      }
      texts.push(
        <text key={`${r}-${xs[0]}`} x={xs.join(" ")} y={y + BASELINE} fill={fill}>
          {str}
        </text>,
      );
      xs = [];
      str = "";
    };
    chars.forEach((ch, c) => {
      const code = ch.charCodeAt(0);
      const x = c * CW;
      const tri = TRIANGLES[code];
      const box = BOX[code];
      if (tri || box) {
        flush();
        if (tri) {
          addRect(LINE, x, y, tri.stub);
          tris.push(
            <polygon
              key={`${r}-${c}`}
              transform={`translate(${x} ${y})`}
              points={tri.points}
              fill={LINE}
            />,
          );
        } else box!.forEach((p) => addRect(LINE, x, y, p));
        return;
      }
      if (ch === " ") {
        flush();
        return;
      }
      if (str && tone[c] !== fill) flush();
      if (!str) fill = tone[c];
      xs.push(x);
      str += ch;
    });
    flush();
  });

  return (
    <svg
      viewBox={`0 0 ${w} ${h}`}
      width={w * scale}
      height={h * scale}
      aria-hidden="true"
      focusable="false"
      className={cn("block h-auto max-w-full", className)}
      style={{ fontFamily: "var(--font-mono), ui-monospace, Menlo, monospace", fontSize: 12 }}
    >
      {[...paths].map(([fill, d]) => (
        <path key={fill} d={d} fill={fill} />
      ))}
      {tris}
      {texts}
    </svg>
  );
}

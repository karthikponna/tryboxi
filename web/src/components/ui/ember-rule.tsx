import { cn } from "@/lib/utils";

const CELL = 8;
const COLS = 240;
const ROWS = 5;
const HEAT = ["#3a180a", "#6b2c0e", "#a2481a", "#e27a38", "#f4a46c"];

function hash(x: number, y: number) {
  const h = Math.sin(x * 127.1 + y * 311.7) * 43758.5453;
  return h - Math.floor(h);
}

// Embers hanging off a section's top edge: densest at the right, thinning out leftward and downward.
const RECTS = (() => {
  const byColor = HEAT.map(() => "");
  for (let r = 0; r < ROWS; r++) {
    for (let c = 0; c < COLS; c++) {
      const fromRight = (COLS - 1 - c) / COLS;
      const reach = Math.max(0, 1 - fromRight / 0.5) ** 1.3;
      const fall = 1 - r / ROWS;
      const p = reach * fall * 1.25;
      const roll = hash(c, r);
      if (roll > p) continue;
      const heat = Math.min(HEAT.length - 1, Math.floor((p - roll) * 2.2 * HEAT.length * hash(c + 31, r + 7)));
      byColor[heat] += `M${c * CELL} ${r * CELL}h${CELL}v${CELL}h-${CELL}z`;
    }
  }
  return byColor;
})();

export function EmberRule({ className }: { className?: string }) {
  return (
    <div aria-hidden="true" className={cn("pointer-events-none absolute inset-x-0 top-0 h-10 overflow-hidden", className)}>
      <svg
        width={COLS * CELL}
        height={ROWS * CELL}
        viewBox={`0 0 ${COLS * CELL} ${ROWS * CELL}`}
        shapeRendering="crispEdges"
        className="absolute top-0 right-0 max-w-none"
      >
        {RECTS.map((d, i) => d && <path key={HEAT[i]} d={d} fill={HEAT[i]} />)}
      </svg>
    </div>
  );
}

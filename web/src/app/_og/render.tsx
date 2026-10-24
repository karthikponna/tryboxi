import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { ImageResponse } from "next/og";

export const ogSize = { width: 1200, height: 630 };
export const ogAlt = "boxi: disposable microVMs for your agents, on your machine";

const GREEN = "#3ee38d";
const LINE = "rgba(226,240,230,0.18)";
const DIM = "rgba(242,246,240,0.38)";
const MUTED = "rgba(242,246,240,0.64)";
const FG = "#f2f6f0";

function Corners({ size = 12, color = LINE }: { size?: number; color?: string }) {
  const line = `1px solid ${color}`;
  const arm = { position: "absolute" as const, display: "flex", width: size, height: size };
  return (
    <>
      <div style={{ ...arm, top: -5, left: -5, borderTop: line, borderLeft: line }} />
      <div style={{ ...arm, top: -5, right: -5, borderTop: line, borderRight: line }} />
      <div style={{ ...arm, bottom: -5, left: -5, borderBottom: line, borderLeft: line }} />
      <div style={{ ...arm, bottom: -5, right: -5, borderBottom: line, borderRight: line }} />
    </>
  );
}

function MiniBox({
  title,
  cmd,
  out,
  outColor,
  status,
  statusColor,
}: {
  title: string;
  cmd: string;
  out: string;
  outColor: string;
  status: string;
  statusColor: string;
}) {
  return (
    <div
      style={{
        position: "relative",
        display: "flex",
        flexDirection: "column",
        width: 290,
        border: `1px solid rgba(226,240,230,0.12)`,
        borderRadius: 10,
        background: "#0b0e0c",
        fontFamily: "Plex",
        fontSize: 17,
      }}
    >
      <Corners />
      <div style={{ display: "flex", padding: "10px 16px", borderBottom: "1px solid rgba(226,240,230,0.1)", color: FG }}>
        {title}
      </div>
      <div style={{ display: "flex", flexDirection: "column", padding: "12px 16px", gap: 4 }}>
        <div style={{ display: "flex", color: FG }}>
          <span style={{ color: DIM, marginRight: 10 }}>$</span>
          {cmd}
        </div>
        <div style={{ display: "flex", color: outColor }}>{out}</div>
        <div style={{ display: "flex", alignItems: "center", color: statusColor, marginTop: 4 }}>
          <div style={{ width: 9, height: 9, borderRadius: 9, background: statusColor, marginRight: 10 }} />
          {status}
        </div>
      </div>
    </div>
  );
}

const dir = join(process.cwd(), "src/app/_og");
const [display, mono] = await Promise.all([
  readFile(join(dir, "FunnelDisplay-Medium.ttf")),
  readFile(join(dir, "IBMPlexMono-Regular.ttf")),
]);

export function renderOg() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          position: "relative",
          background: "#060807",
          backgroundImage: "radial-gradient(circle at 78% 58%, rgba(62,227,141,0.20), rgba(6,8,7,0) 52%)",
          padding: 64,
        }}
      >
        <div style={{ display: "flex", flexDirection: "column", justifyContent: "space-between", width: 680 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 16 }}>
            <svg width="40" height="40" viewBox="0 0 20 20">
              <path
                d="M 13.5 2.75 L 2.75 2.75 L 2.75 17.25 L 17.25 17.25 L 17.25 6.5"
                fill="none"
                stroke={FG}
                strokeWidth="1.5"
                strokeLinecap="square"
              />
              <rect x="15.25" y="0.75" width="4" height="4" fill={GREEN} />
            </svg>
            <div style={{ fontFamily: "Funnel", fontSize: 44, color: FG, letterSpacing: "-0.02em" }}>boxi</div>
          </div>
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              fontFamily: "Funnel",
              fontSize: 70,
              lineHeight: 1.04,
              letterSpacing: "-0.035em",
              color: FG,
            }}
          >
            <div style={{ display: "flex" }}>Disposable microVMs</div>
            <div style={{ display: "flex" }}>for your agents. On</div>
            <div style={{ display: "flex", alignItems: "center" }}>
              your machine.
              <div style={{ width: 20, height: 56, background: GREEN, marginLeft: 10, marginTop: 6 }} />
            </div>
          </div>
          <div style={{ display: "flex", fontFamily: "Plex", fontSize: 22, color: MUTED, gap: 18 }}>
            <span>sandboxd</span>
            <span style={{ color: DIM }}>·</span>
            <span>sandbox-mcp</span>
            <span style={{ color: DIM }}>·</span>
            <span>$0 cloud bill</span>
          </div>
        </div>
        <div
          style={{
            position: "absolute",
            right: 64,
            top: 56,
            display: "flex",
            flexDirection: "column",
            gap: 20,
          }}
        >
          <MiniBox title="sbx_7f3" cmd="npm test" out="12 passed" outColor={GREEN} status="running" statusColor={GREEN} />
          <div style={{ display: "flex", marginLeft: 60 }}>
            <MiniBox
              title="sbx_2c9"
              cmd="docker run nginx"
              out=":8080 up"
              outColor={MUTED}
              status="paused"
              statusColor="#f2b94b"
            />
          </div>
          <MiniBox title="sbx_e41" cmd="curl evil.example" out="blocked" outColor="#ff6b6b" status="running" statusColor={GREEN} />
        </div>
      </div>
    ),
    {
      ...ogSize,
      fonts: [
        { name: "Funnel", data: display, weight: 500, style: "normal" },
        { name: "Plex", data: mono, weight: 400, style: "normal" },
      ],
    },
  );
}

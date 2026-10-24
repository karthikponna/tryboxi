import { ImageResponse } from "next/og";

export const size = { width: 180, height: 180 };
export const contentType = "image/png";

export default function AppleIcon() {
  return new ImageResponse(
    (
      <div style={{ width: "100%", height: "100%", display: "flex", background: "#060807" }}>
        <svg width="180" height="180" viewBox="0 0 32 32">
          <path
            d="M 19.6 7.4 L 7.4 7.4 L 7.4 24.6 L 24.6 24.6 L 24.6 12"
            fill="none"
            stroke="#f2f6f0"
            strokeWidth="2.2"
            strokeLinecap="square"
          />
          <rect x="21.4" y="5.2" width="5.4" height="5.4" fill="#3ee38d" />
        </svg>
      </div>
    ),
    size,
  );
}

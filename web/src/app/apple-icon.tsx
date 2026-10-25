import { ImageResponse } from "next/og";
import { LOGO_PATH } from "@/components/ui/logo";

export const size = { width: 180, height: 180 };
export const contentType = "image/png";

export default function AppleIcon() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          background: "#090605",
        }}
      >
        <svg width="108" height="94" viewBox="0 0 30 26">
          <path d={LOGO_PATH} fill="#e27a38" />
        </svg>
      </div>
    ),
    size,
  );
}

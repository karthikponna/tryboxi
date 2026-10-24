import { ogAlt, ogSize, renderOg } from "./_og/render";

export const alt = ogAlt;
export const size = ogSize;
export const contentType = "image/png";

export default function Image() {
  return renderOg();
}

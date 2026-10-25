import type { MetadataRoute } from "next";
import { site } from "@/site.config";

export default function sitemap(): MetadataRoute.Sitemap {
  return [
    { url: site.url, changeFrequency: "weekly", priority: 1 },
    { url: `${site.url}/llms.txt`, changeFrequency: "weekly", priority: 0.5 },
  ];
}

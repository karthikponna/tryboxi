import { cacheLife } from "next/cache";
import { site } from "@/site.config";

// Live stargazer count, refreshed hourly. Null when GitHub can't be reached, so the UI
// shows no number rather than a made-up one.
export async function getStarCount(): Promise<number | null> {
  "use cache";
  cacheLife("hours");
  try {
    const res = await fetch(`https://api.github.com/repos/${site.repo.owner}/${site.repo.name}`, {
      headers: { Accept: "application/vnd.github+json" },
      signal: AbortSignal.timeout(4000),
    });
    if (!res.ok) return null;
    const data = (await res.json()) as { stargazers_count?: unknown };
    return typeof data.stargazers_count === "number" ? data.stargazers_count : null;
  } catch {
    return null;
  }
}

export function formatStars(n: number) {
  if (n < 1000) return String(n);
  const k = n / 1000;
  return `${k >= 100 ? Math.round(k) : k.toFixed(1).replace(/\.0$/, "")}k`;
}

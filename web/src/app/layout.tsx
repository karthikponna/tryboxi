import type { Metadata, Viewport } from "next";
import { Funnel_Display, IBM_Plex_Mono, Inter } from "next/font/google";
import { Analytics } from "@vercel/analytics/next";
import { site } from "@/site.config";
import "./globals.css";

const display = Funnel_Display({
  subsets: ["latin"],
  variable: "--font-display",
  display: "swap",
});

const sans = Inter({
  subsets: ["latin"],
  variable: "--font-sans",
  display: "swap",
});

const mono = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500"],
  variable: "--font-mono",
  display: "swap",
});

export const metadata: Metadata = {
  metadataBase: new URL(site.url),
  title: site.title,
  description: site.description,
  applicationName: site.name,
  alternates: { canonical: "/" },
  keywords: [
    "microVM",
    "sandbox",
    "AI agents",
    "MCP",
    "Cursor",
    "Claude Code",
    "Codex",
    "Apple Virtualization.framework",
    "local sandbox",
  ],
  openGraph: {
    type: "website",
    url: "/",
    siteName: site.name,
    title: site.title,
    description: site.description,
  },
  twitter: {
    card: "summary_large_image",
    title: site.title,
    description: site.description,
  },
};

export const viewport: Viewport = {
  themeColor: "#060807",
  colorScheme: "dark",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`dark ${display.variable} ${sans.variable} ${mono.variable}`}
    >
      <body className="bg-bg text-fg">
        {children}
        <Analytics />
      </body>
    </html>
  );
}

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
  authors: [{ name: site.author, url: site.social.x }],
  creator: site.author,
  publisher: site.author,
  category: "Developer Tools",
  alternates: { canonical: "/" },
  keywords: [
    "tryboxi",
    "AI agent sandbox",
    "local sandbox for AI agents",
    "microVM",
    "disposable Linux VM",
    "code execution sandbox",
    "secure code execution",
    "MCP server",
    "Cursor MCP",
    "Claude Code MCP",
    "Codex MCP",
    "E2B alternative",
    "Apple Virtualization.framework",
    "Apple Silicon",
    "macOS sandbox",
    "open source",
  ],
  robots: {
    index: true,
    follow: true,
    googleBot: {
      index: true,
      follow: true,
      "max-image-preview": "large",
      "max-snippet": -1,
      "max-video-preview": -1,
    },
  },
  openGraph: {
    type: "website",
    locale: "en_US",
    url: "/",
    siteName: site.name,
    title: site.title,
    description: site.description,
  },
  twitter: {
    card: "summary_large_image",
    title: site.title,
    description: site.description,
    creator: site.social.xHandle,
  },
  // set in Vercel once Google Search Console gives you a verification token
  ...(process.env.NEXT_PUBLIC_GOOGLE_SITE_VERIFICATION && {
    verification: { google: process.env.NEXT_PUBLIC_GOOGLE_SITE_VERIFICATION },
  }),
};

export const viewport: Viewport = {
  themeColor: "#090605",
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

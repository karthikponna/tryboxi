# tryboxi.xyz

The tryboxi landing page: Next.js (App Router), Tailwind CSS v4, shadcn/ui on Radix, three.js for the hero.

## Run

```bash
bun install
bun run dev     # http://localhost:3000
bun run lint
bun run build && bun run start
```

## Where things are

```text
src/
├── site.config.ts        # repo URL, site URL, social links, install commands
├── app/                  # layout, page, metadata routes (OG image, icon, robots, sitemap)
└── components/
    ├── hero/             # hero layout, dither background, iMac + Mac mini scene
    ├── sections/         # header, architecture, speed, connect, FAQ, footer
    └── ui/               # shared building blocks (BoxFrame, InstallBox, CodeTabs, ...)
```

## Deploy

Import the repo on Vercel with **Root Directory** set to `web`. Vercel detects `bun.lock` and builds with bun.
Enable Web Analytics in the project's Analytics tab.

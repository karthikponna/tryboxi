// Every URL and install string on the site lives here. Switching to a one-line
// installer later means replacing `install.macos` with a single command.

const repoUrl = "https://github.com/karthikponna/tryboxi";

export const site = {
  name: "boxi",
  url: "https://tryboxi.dev",
  title: "boxi · Disposable microVMs for your agents, on your machine",
  description:
    "Give Cursor, Claude Code, and your own agents a real Linux machine to run tests, installs, servers, and untrusted code. Local microVMs on your Mac. Your files and secrets stay out of reach.",
  repo: {
    owner: "karthikponna",
    name: "tryboxi",
    url: repoUrl,
  },
  social: {
    x: "https://x.com/karthikponna19",
  },
  install: {
    macos: [
      `git clone ${repoUrl} && cd tryboxi && make build`,
      "./_out/bin/sandboxd install --artifacts _out",
      "./_out/bin/sandboxctl mcp install cursor --project <your-repo>",
    ],
  },
} as const;

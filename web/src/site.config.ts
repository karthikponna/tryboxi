// Every URL and install string on the site lives here. Switching to a one-line
// installer later means replacing `install.macos` with a single command.

const repoUrl = "https://github.com/karthikponna/tryboxi";
const bin = "./_out/bin";
// example repo the agent works in; the MCP output lines on the site use the same path
const project = "~/code/app";

export const site = {
  name: "tryboxi",
  url: "https://tryboxi.xyz",
  title: "tryboxi · Disposable microVMs for your agents, on your machine",
  description:
    "tryboxi gives Cursor, Claude Code, Codex, and your own AI agents disposable Linux microVMs on your Mac to run tests, installs, servers, and untrusted code securely. Free and open source.",
  tagline: "Disposable Linux microVMs on your own machine, for AI agents.",
  repo: {
    owner: "karthikponna",
    name: "tryboxi",
    url: repoUrl,
  },
  author: "Karthik Ponna",
  social: {
    x: "https://x.com/karthikponna19",
    xHandle: "@karthikponna19",
  },
  install: {
    macos: [`git clone ${repoUrl} && cd tryboxi`, "make images build", `${bin}/sandboxd install --artifacts _out`],
    project,
    mcp: {
      cursor: `${bin}/sandboxctl mcp install cursor --project ${project}`,
      claude: `${bin}/sandboxctl mcp install claude --project ${project}`,
      codex: `${bin}/sandboxctl mcp install codex --project ${project}`,
    },
  },
} as const;

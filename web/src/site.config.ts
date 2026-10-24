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
    api: "https://api.github.com/repos/karthikponna/tryboxi",
  },
  links: {
    docs: `${repoUrl}#readme`,
    testing: `${repoUrl}/blob/main/docs/TESTING.md`,
    openapi: `${repoUrl}/blob/main/api/openapi.yaml`,
    goSdk: `${repoUrl}/tree/main/pkg/client`,
    examples: `${repoUrl}/tree/main/examples`,
    agentRules: `${repoUrl}/tree/main/agent-rules`,
    grafana: `${repoUrl}/tree/main/deploy/observability`,
    issues: `${repoUrl}/issues`,
  },
  install: {
    macos: [
      `git clone ${repoUrl} && cd tryboxi && make build`,
      "./_out/bin/sandboxd install --artifacts _out",
      "./_out/bin/sandboxctl mcp install cursor --project <your-repo>",
    ],
    linux: null,
    windows: null,
  },
} as const;

export type InstallOs = keyof typeof site.install;

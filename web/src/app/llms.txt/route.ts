import { site } from "@/site.config";

// A plain-text summary for AI agents and LLM crawlers (https://llmstxt.org).
const body = `# ${site.name}

> ${site.tagline}

${site.description}

Each sandbox is its own Ubuntu 24.04 microVM on Apple's Virtualization.framework. Your repo syncs in one way,
changes come back only through diff / apply / pull with a backup, network access is allowlist-only, and stored
secrets never enter the VM. It runs on macOS 14+ on Apple Silicon and is MIT licensed.

## Install

\`\`\`bash
${site.install.macos.join("\n")}
\`\`\`

## Connect an agent over MCP

\`\`\`bash
${site.install.mcp.cursor}
${site.install.mcp.claude}
${site.install.mcp.codex}
\`\`\`

## Links

- [Website](${site.url})
- [Source code and README](${site.repo.url})
- [HTTP API contract (OpenAPI)](${site.repo.url}/blob/main/api/openapi.yaml)
- [Author on X](${site.social.x})
`;

export function GET() {
  return new Response(body, { headers: { "Content-Type": "text/plain; charset=utf-8" } });
}

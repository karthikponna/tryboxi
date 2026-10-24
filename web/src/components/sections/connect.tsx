import { CodeTabs } from "@/components/ui/code-tabs";
import { Section, SectionHeader } from "@/components/ui/section-header";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { SNIPPETS } from "./connect-snippets";

const TOOLS: [string, string][] = [
  ["sandbox_exec", "Run a shell command in the microVM. The repo is mirrored at /workspace and synced before every run."],
  ["sandbox_read_file", "Read a file from inside the sandbox, such as build output."],
  ["sandbox_write_file", "Write a file inside the sandbox only. The host is not touched."],
  ["sandbox_diff", "Show changes in /workspace relative to its git HEAD."],
  ["sandbox_apply", "Merge the sandbox's changes into your repo, backing up every touched file first."],
  ["sandbox_pull", "Copy files that commands changed, like lockfiles or codegen, back to your repo with a backup."],
  ["sandbox_snapshot", "Checkpoint, restore, or fork the sandbox for parallel attempts."],
  ["sandbox_network", "Ask to open access to a blocked host. You approve the call."],
  ["sandbox_port", "Expose a server's port on the host's 127.0.0.1 and return the URL."],
  ["sandbox_status", "Show the session's sandbox, recent commands, and host capacity."],
  ["sandbox_destroy", "Destroy the sandbox and free its memory."],
];

export function Connect() {
  return (
    <Section id="connect" labelledBy="connect-title">
      <SectionHeader
        id="connect-title"
        eyebrow="05 · Connect"
        title="Connect your agent"
        lead="One command writes the MCP config and agent rules for Cursor, Claude Code, or Codex. Anything that speaks HTTP can use the same API on the unix socket."
      />
      <div className="mt-12 lg:mt-16" data-reveal>
        <CodeTabs snippets={SNIPPETS} />
      </div>
      <div className="mt-8 flex flex-col gap-3 lg:flex-row lg:items-baseline lg:gap-6" data-reveal>
        <p className="shrink-0 font-mono text-[11.5px] leading-none font-medium tracking-[0.12em] text-fg-dim uppercase">
          MCP tools
        </p>
        <ul className="-mx-1.5 flex flex-wrap gap-x-1 gap-y-1 font-mono text-[12.5px] leading-[1.5] text-fg-muted">
          {TOOLS.map(([name, desc]) => (
            <li key={name}>
              <Tooltip>
                <TooltipTrigger asChild>
                  <button
                    type="button"
                    className="rounded-[4px] px-1.5 py-0.5 transition-colors duration-150 hover:bg-panel-2 hover:text-fg data-[state=delayed-open]:text-fg data-[state=instant-open]:text-fg"
                  >
                    {name}
                  </button>
                </TooltipTrigger>
                <TooltipContent side="top">{desc}</TooltipContent>
              </Tooltip>
            </li>
          ))}
        </ul>
      </div>
    </Section>
  );
}

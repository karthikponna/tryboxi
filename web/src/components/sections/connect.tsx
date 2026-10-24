import { CodeTabs } from "@/components/ui/code-tabs";
import { Section, SectionHeader } from "@/components/ui/section-header";
import { SNIPPETS } from "./connect-snippets";

export function Connect() {
  return (
    <Section id="connect" labelledBy="connect-title">
      <SectionHeader
        id="connect-title"
        eyebrow="03 · Connect"
        title="Connect your agent"
        lead="One command writes the MCP config and agent rules for Cursor, Claude Code, or Codex. Anything that speaks HTTP can use the same API on the unix socket."
      />
      <div className="mt-12 lg:mt-16" data-reveal>
        <CodeTabs snippets={SNIPPETS} />
      </div>
    </Section>
  );
}

import { t } from "../../i18n";
import type { RepositoryTaskWorkflow } from "../../lib/task-repositories";

const guide = "docs/extensions/repository-providers.md";

type RepositoryDetails = {
  url: string;
  name: string;
  aliases: string;
  purpose: string;
  task_workflow: "" | RepositoryTaskWorkflow;
};

function addressForDraft(entered: string) {
  // Queries and fragments can carry provider-specific secrets. Keep the original scheme and path spelling.
  const address = entered.split(/[?#]/, 1)[0];
  if (/[\s\\\p{Cc}]/u.test(address)) return;
  const scp = /^[^/:@]+@([^/:@]+):(.+)$/.exec(address);
  const candidate = scp ? `ssh://${scp[1]}/${scp[2].replace(/^\//, "")}` : address;
  const parts = /^((?:https?|ssh):\/\/)([^/]+)(.*)$/i.exec(candidate);
  if (!parts) return;
  const [, scheme, authority, path] = parts;
  const hostAndPort = authority.slice(authority.lastIndexOf("@") + 1);
  try {
    // Validate just the authority, without normalizing the path. Use the same host rules for SSH and HTTP.
    const { hostname } = new URL(`https://${hostAndPort}`);
    return { url: scheme + hostAndPort + path, host: hostname };
  } catch { return; }
}

export function repositoryAssistantPrompt(details?: RepositoryDetails): string {
  if (!details?.url.trim()) return t("settings-repositories.assistant_prompt_source", { guide });

  const address = addressForDraft(details.url.trim());
  const lines = address ? [
    t("settings-repositories.assistant_prompt_url", { value: address.url }),
    t("settings-repositories.assistant_prompt_host", { value: address.host }),
  ] : [t("settings-repositories.assistant_prompt_need_url")];

  const name = details.name.trim();
  const aliases = details.aliases.split(",").map((alias) => alias.trim()).filter(Boolean).join(", ");
  const purpose = details.purpose.trim();
  if (name) lines.push(t("settings-repositories.assistant_prompt_name", { value: name }));
  if (aliases) lines.push(t("settings-repositories.assistant_prompt_aliases", { value: aliases }));
  if (purpose) lines.push(t("settings-repositories.assistant_prompt_purpose", { value: purpose }));
  if (details.task_workflow) lines.push(t("settings-repositories.task_workflow_row", { value: details.task_workflow }));

  return `${t("settings-repositories.assistant_prompt_bind", { guide })}\n\n${lines.join("\n")}`;
}

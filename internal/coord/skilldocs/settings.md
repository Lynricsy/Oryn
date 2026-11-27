- Global agent configuration files apply to every agent, so writing them needs confirmation; `path` is relative to the configuration root and `body` is the file text.
- Secret values are write-only.

## Agent configuration layout
Paths are relative to the configuration root:
- `skills/<name>/SKILL.md` — a skill. Its frontmatter needs `description`; `name` defaults to the directory name. Supporting files (`scripts/`, `references/`, `assets/`, …) sit in the same directory and SKILL.md refers to them by relative path.
- `commands/<name>.md`, `rules/<name>.md` or `.mdc`, `prompts/<name>.md`, `instructions/<name>.md` — one file each.
- `extensions/<name>.ts` or `.js`, or a directory `extensions/<name>/` with `index.ts` / `index.js`, or a `package.json` whose `omp.extensions` lists the entry points.
- `tools/<name>.json|md|ts|js|sh|bash|py`, or a directory `tools/<name>/` with `index.ts`.
- `hooks/pre/<tool>.<ext>` and `hooks/post/<tool>.<ext>`.
- Top-level files such as `AGENTS.md`, `SYSTEM.md`, `RULES.md`, `config.yml`.

## Enabling and disabling
`config.yml` key `disabledExtensions` lists disabled items: `skill:<name>`, `slash-command:<name>`, `rule:<name>`, `prompt:<name>`, `instruction:<name>`, `extension-module:<name>`, `tool:<name>`, `hook:<pre|post>:<tool>:<file>`. Read `config.yml` with `getAgentConfigFile`, change the list, and put the whole file back with `putAgentConfigFile`, keeping every other key.

## Multi-file entries
- Use `applyAgentConfigBatch` to write a whole skill, extension or tool directory in one request and one confirmation. It deletes `delete` paths first (a directory path removes every file under it; unmatched paths are ignored), then writes `files`; the whole batch is validated before anything changes.
- Replace an entry: in the same batch, `delete` its directory and write all of its files. Remove an entry: `delete` its directory.
- Binary files: `"encoding": "base64"`. Executable scripts: `"mode": 493` (0755).
- Single-file edits can still use `putAgentConfigFile` / `deleteAgentConfigFile`.

## When changes take effect
Changes to extensions, hooks, tools, `SYSTEM.md` and similar need a session restart. `getAgentConfigApplyStatus` lists the sessions that need one; `restartAgentConfigSessions` restarts them.

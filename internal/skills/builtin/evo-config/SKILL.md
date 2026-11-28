---
name: evo-config
description: Use when the user needs help configuring Evo — writing evorc (the Bash config format) or evo.json, setting up providers, models, LSPs, MCP servers, hooks, skills, permissions, or changing Evo behavior.
---

# Evo Configuration

Evo supports two config formats:

- **`evorc`** — a Bash script that builds config by calling Evo builtins.
  **Preferred.** Because it is real Bash you get includes, secrets,
  conditionals, and variables for free.
- **`evo.json`** — static JSON. Fully supported; see
  [Legacy JSON format](#legacy-json-format).

Both are discovered together and deep-merged. Priority (highest to lowest):

1. `.evorc` / `evorc` / `.evo.json` / `evo.json` (project-local,
   closer-to-cwd wins; Windows uses `.\.evorc` / `.\evorc`)
2. `$XDG_CONFIG_HOME/evo/evorc` or `~/.config/evo/evorc`
   (`%XDG_CONFIG_HOME%\evo\evorc` or
   `%USERPROFILE%\.config\evo\evorc` on Windows)

Data directories (`~/.local/share/evo` and `%LOCALAPPDATA%\evo`) contain
machine-owned JSON state only; Evo does not discover or execute a `evorc`
from those locations.

If a directory has both `evorc` and `evo.json`, they merge (`evorc` wins
on conflicts) and Evo logs a warning.

## evorc at a glance

A `evorc` is a plain Bash script executed at load time with the same embedded
shell the `bash` tool uses. It builds config by calling builtins (`provider`,
`model`, `mcp`, `lsp`, `hook`, `permissions`, `option`). Statements run top to
bottom; later statements win, and `remove`/`reset` operate on anything defined
earlier or pulled in via `source`.

```bash
#!/usr/bin/env bash
# Includes and secrets are just Bash.
source ~/.config/evo/shared.sh

provider add anthropic --api-key "$ANTHROPIC_API_KEY"

model large anthropic/claude-sonnet-4-20250514 --max-tokens 16384
model small anthropic/claude-haiku-4-20250514

option skill-path ./skills
permissions allow view ls grep edit
```

Values are ordinary Bash — quote and expand normally (`"$VAR"`, `$(cmd)`,
`${VAR:?required}`). A failing `$(command)` aborts the load.

`EVO_VERSION` is exported into the script so you can feature-detect the
running Evo (it is the literal `devel` for local builds):

```bash
[[ "$EVO_VERSION" != devel ]] && lsp add gopls --command gopls
```

## Commands

All entity commands are verb-first. `remove` accepts `rm` as an alias. Booleans
accept `true/false/1/0/yes/no`, case-insensitive.

### providers

```bash
provider add <id> [flags]    # define/update; repeated calls merge
provider remove <id>         # alias: rm — removes the provider and its models
```

Flags: `--name`, `--type` (`completions`, `responses`, `messages`, or a local
type like `ollama`, `lmstudio`, `llamacpp`; legacy values `openai`, `openai-compat`,
`anthropic` are migrated automatically), `--api-key`, `--base-url`,
`--disable BOOL`, `--flat-rate BOOL`, `--discover-models BOOL`,
`--system-prompt-prefix TEXT`, `--extra-header KEY VALUE` (repeatable),
`--extra-body JSON`, `--provider-options JSON`, `--aws-auth-refresh CMD`
(Bedrock only: command run when AWS credentials expire).

```bash
provider add deepseek \
  --type completions \
  --base-url "https://api.deepseek.com/v1" \
  --api-key "${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY}"
```

### models

```bash
model add <provider>/<id> [flags]      # register a custom model (provider must exist)
model remove <provider>/<id>           # alias: rm
model large [<provider>/<id>] [flags]  # set the large slot; no arg prints it
model small [<provider>/<id>] [flags]  # set the small slot; no arg prints it
```

- `<provider>/<id>` is the same form `evo models` prints. A missing slash is
  an error. `model add` requires the provider to already exist.
- `model add` flags: `--name`, `--context-window N`, `--default-max-tokens N`,
  `--thinking always|never|toggleable`, `--supports-images BOOL`,
  `--price-input F`, `--price-output F`, `--price-cache-create F`,
  `--price-cache-hit F`, `--reasoning-effort low|medium|high`,
  `--max-attachments N`.
- Image input is enabled per model with `--supports-images true`, which maps to
  `capabilities.vision: true` in `evo.json`. Without it, Evo filters image
  attachments out before sending the request, so the model only sees text.
  `--max-attachments N` caps how many attachments the TUI lets you queue for a
  single request; `0`/omitted means no hard cap.
- `model large`/`model small` flags: `--think`, `--reasoning-effort`,
  `--max-tokens N`, `--temperature F`, `--top-p F`, `--top-k N`,
  `--frequency-penalty F`, `--presence-penalty F`, `--provider-options JSON`.
- `model large` with no argument prints the current selection as `provider/id`,
  usable in `$(model large)`.

`large` is the primary coding model; `small` is used for summarization.

### mcp

```bash
mcp add <name> --type stdio|sse|http [flags]   # default type is stdio
mcp remove <name>                              # alias: rm
```

Flags: `--command CMD`, `--args ARG` (repeatable), `--env KEY VALUE`
(repeatable), `--url URL`, `--header KEY VALUE` (repeatable), `--timeout N`,
`--disabled BOOL`, `--disabled-tools TOOL` (repeatable), `--enabled-tools TOOL`
(repeatable), `--sessionless BOOL`, `--oauth BOOL`, `--oauth-client-id ID`, `--oauth-client-secret SECRET`,
`--oauth-callback-port PORT`, `--channel BOOL` (opt the server in as a
channel, like `--channels` on the CLI), `--channel-reply JSON` (the
`channel_reply` routing object).

```bash
mcp add github --type http \
  --url "https://api.githubcopilot.com/mcp/" \
  --header Authorization "Bearer $GH_PAT"

mcp add filesystem --command node --args /path/to/mcp-server.js
```

### lsp

```bash
lsp add <name> --command CMD [flags]
lsp remove <name>                     # alias: rm
```

Flags: `--args ARG` (repeatable), `--env KEY VALUE` (repeatable),
`--filetypes TYPE` (repeatable), `--root-markers MARKER` (repeatable),
`--timeout N`, `--disabled BOOL`, `--init-options JSON`, `--options JSON`.

```bash
lsp add go --command gopls --env GOPATH "$HOME/go"
lsp add typescript --command typescript-language-server --args --stdio
```

### hooks

```bash
hook add <event> --command CMD [--name NAME] [--matcher REGEX] [--timeout N]
hook remove <event> [--name NAME]    # alias: rm; without --name clears the event
```

Only named hooks can be removed individually — give a hook `--name` if you
intend to remove it later. See [Hooks runtime](#hooks-runtime) for how hooks
execute (stdin payload, env vars, decisions).

```bash
hook add PreToolUse --matcher "^bash$" --command ".evo/hooks/no-haskell.sh" --name no-haskell
```

### permissions

```bash
permissions allow <tool> [<tool> ...]   # tools that skip permission prompts
permissions deny <tool> [<tool> ...]    # hide tools from the agent entirely
```

`deny` is the inverse of `allow`: it writes `options.disabled_tools`. A denied
tool is hidden from the agent, not merely prompted for.

### options

```bash
option <key> [value]
option reset <list-key>    # clear a list option back to empty
```

- **Boolean keys** (value optional, defaults `true`): `debug`, `debug-lsp`,
  `auto-lsp`, `progress`, `pinentry-integrated`.
- **Boolean keys phrased positively** (stored as the negated field): `metrics`,
  `auto-summarize`, `provider-auto-update`,
  `default-providers`. Example: `option metrics false` disables metrics.
- **String keys**: `data-directory`, `initialize-as`, `notifications`.
- **Integer keys**: `request-timeout`, `max-background-jobs`,
  `pinentry-cache-timeout`.
- **Attribution keys**: `attribution-trailer-style` (`none`, `co-authored-by`,
  `assisted-by`) and `attribution-generated-with` (boolean).
- **UI settings**: `option ui compact BOOL`, `option ui diff unified|split`,
  `option ui transparent BOOL`, `option ui mouse BOOL` (default `true`;
  disable to let the terminal/tmux handle selection, copy/paste, and
  scrolling), `option ui nerd-fonts BOOL`, `option ui scrollbar default|always|never`,
  `option ui exit-banner default|compact|none`,
  `option ui completions-max-depth N`, `option ui completions-max-items N`.
- **Environment**: `export FOO=bar` in a `evorc` sets `FOO` in Evo's
  environment on startup (the top-level `env` object in JSON). Only vars
  the script exports or changes count.
- **Prompt overrides**: `option prompt <name> <path>` replaces a built-in
  prompt (`coder`, `task`, `plan`, `summary`, `title`, `initialize`) with a
  file. Relative paths resolve against the directory of the config file
  that sets them. The file replaces the built-in whole, so a `coder`
  replacement must include `{{.AvailSkillXML}}` and a
  `{{range .ContextFiles}}<file path="{{.Path}}">{{.Content}}</file>{{end}}`
  loop (copy the blocks from the built-in `coder.md.tpl`) to keep them. For
  one run, use `evo --prompt coder=./file.md.tpl` instead.
- **Experimental** (unfinished, may change or disappear in any release):
  `option experimental client-server BOOL` runs the agent in a background
  server the TUI attaches to; `option experimental server-ready-timeout N`,
  `server-idle-timeout N` (default 60), and `server-detach-grace N` (default
  10) are in seconds. The env vars `EVO_CLIENT_SERVER`,
  `EVO_SERVER_READY_TIMEOUT`, `EVO_SERVER_IDLE_TIMEOUT`, and
  `EVO_SERVER_DETACH_GRACE` take priority when set.
- **List keys** (singular, one value per call, repeatable): `context-path`,
  `global-context-path`, `skill-path`, `disable-skill`. Use `option reset <key>`
  to wipe inherited values (e.g. after `source`).

```bash
option progress false
option skill-path ./skills
option disable-skill evo-config
option attribution-trailer-style assisted-by
option attribution-generated-with true
option ui compact true
option ui diff unified
option ui exit-banner compact
```

> [!IMPORTANT] These skill paths are loaded by default and do NOT need
> `skill-path`: `.agents/skills`, `.evo/skills`, `.claude/skills`,
> `.cursor/skills`.

## Hooks runtime

Hooks are user-defined shell commands that fire on agent events. Evo supports
13 events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`,
`PermissionRequest`, `PermissionResult`, `SessionStart`, `SessionEnd`,
`TurnStart`, `TurnEnd`, `StopFailure`, `Interrupt`, `PreCompact`, and
`PostCompact`. `--matcher` is only accepted on the first five. This behavior
is the same however the hook is defined (`hook add` or JSON).

### How hooks work

1. When a tool is about to be called, all `PreToolUse` hooks with a matching
   `matcher` (or no matcher) run in parallel.
2. Duplicate commands are deduplicated — each unique command runs at most once.
3. The hook receives JSON on **stdin** and hook-specific **environment
   variables**.

Event names are case-insensitive and accept snake_case: `PreToolUse`,
`pretooluse`, `pre_tool_use`, `PRE_TOOL_USE` all work.

### Hook input (stdin)

```json
{
  "event": "PreToolUse",
  "hook_event_name": "PreToolUse",
  "session_id": "abc-123",
  "cwd": "/path/to/project",
  "tool_name": "bash",
  "tool_input": { "command": "ls -la" },
  "tool_use_id": "call_01"
}
```

Events without a tool call omit the `tool_*` fields. Some add one of their
own: `tool_response` (PostToolUse), `error` (PostToolUseFailure,
StopFailure), `last_assistant_message` (TurnEnd), `decision`
(PermissionResult), `source` (SessionStart), `reason` (SessionEnd), and
`trigger` (PreCompact, PostCompact).

### Hook environment variables

| Variable                     | Description                                       |
| ---------------------------- | ------------------------------------------------- |
| `EVO_EVENT`                | Event name (e.g. `PreToolUse`)                    |
| `EVO_TOOL_NAME`            | Name of the tool being called                     |
| `EVO_SESSION_ID`           | Current session ID                                |
| `EVO_CWD`                  | Current working directory                         |
| `EVO_PROJECT_DIR`          | Project root directory                            |
| `CLAUDE_PROJECT_DIR`         | Same value, for scripts written for Claude Code   |
| `EVO_TOOL_INPUT_COMMAND`   | Value of `command` from tool input (if present)   |
| `EVO_TOOL_INPUT_FILE_PATH` | Value of `file_path` from tool input (if present) |

### Hook output

**Exit code 0** — hook succeeded. Stdout is parsed as JSON:

```json
{ "decision": "allow", "context": "optional context appended to tool result" }
```

- `decision`: `allow` to explicitly allow, `deny` to block, `none` (or omit).
- `reason`: explanation (used when denying).
- `context`: extra context appended to the tool result.
- `updated_input`: on PreToolUse, a shallow-merge patch against the tool
  input.
- `updated_output`: on PostToolUse, a string that replaces the tool output.
- `halt`: `true` ends the turn (blocks compaction on PreCompact).

**Exit code 2** — the tool call is blocked; stderr is the deny reason.

**Exit code 49** — the whole turn is halted; stderr is the reason.

**Any other exit code** — non-blocking error; the tool call proceeds, unless
stdout holds a JSON object, which is applied anyway.

### Decision aggregation

- **Deny wins over allow** — any deny blocks the call.
- **Allow wins over none** — a lone allow lets it proceed.
- Deny reasons and context strings are concatenated (newline-separated).
- `updated_input` patches shallow-merge in config order on PreToolUse;
  `updated_output` on PostToolUse is last-one-wins.

### Claude Code compatibility

Evo also accepts the Claude Code hook output formats (`hookSpecificOutput`,
top-level `decision: "block"` with `reason`, `continue: false`), so existing
hooks work unchanged:

```json
{
  "hookSpecificOutput": {
    "permissionDecision": "allow",
    "permissionDecisionReason": "Auto-approved",
    "updatedInput": { "command": "echo rewritten" }
  }
}
```

## User-invocable skills

Skills can be invoked as commands. Add `user-invocable: true` to the skill's
YAML frontmatter:

```yaml
---
name: my-skill
description: A skill that can be invoked as a command.
user-invocable: true
---
```

- Global skills appear as `user:skill-name`; project skills as
  `project:skill-name`.
- Add `disable-model-invocation: true` to keep a skill user-only (hidden from
  the model's available-skills list but still manually invocable).

## Environment variables

- `EVO_VERSION` — exported into `evorc` at load; the running version (or
  `devel` for local builds).
- `EVO_GLOBAL_CONFIG` — override the global config directory (`evorc` and
  `evo.json` are looked up under it).
- `EVO_GLOBAL_DATA` — override the machine data directory.
- `EVO_DATA_DIR` — override the per-project data directory.
- `EVO_CACHE_DIR` — override the cache directory.
- `EVO_SKILLS_DIR` — override the default skills directory.

## Legacy JSON format

`evo.json` is the original static format. It still works and merges with
`evorc`. Basic structure:

```json
{
  "$schema": "https://raw.githubusercontent.com/Teresa487/evo/main/schema.json",
  "models": {},
  "providers": {},
  "mcp": {},
  "lsp": {},
  "hooks": {},
  "options": {},
  "permissions": {}
}
```

The `$schema` property enables IDE autocomplete but is optional.

### evorc ↔ evo.json mapping

| evorc                             | evo.json                                             |
| ------------------------------------ | ------------------------------------------------------ |
| `provider add openai --api-key "$K"` | `providers.openai = {"api_key": "$K"}`                 |
| `model add openai/gpt-x --name X`    | append to `providers.openai.models[]`                  |
| `model large openai/gpt-x`           | `models.large = {"provider":"openai","model":"gpt-x"}` |
| `mcp add gh --type http --url U`     | `mcp.gh = {"type":"http","url":"U"}`                   |
| `lsp add go --command gopls`         | `lsp.go = {"command":"gopls"}`                         |
| `hook add PreToolUse --command C`    | append to `hooks.PreToolUse[]`                         |
| `permissions allow view ls`          | `permissions.allowed_tools = ["view","ls"]`            |
| `permissions deny bash`              | `options.disabled_tools = ["bash"]`                    |
| `option skill-path ./skills`         | `options.skills_paths = ["./skills"]`                  |
| `option metrics false`               | `options.disable_metrics = true`                       |
| `option request-timeout 300`          | `options.request_timeout = 300`                        |
| `option max-background-jobs 200`     | `options.max_background_jobs = 200`                    |
| `export AWS_PROFILE=work`            | `env.AWS_PROFILE = "work"`                             |
| `option prompt coder ./coder.md.tpl` | `options.prompts.coder = "./coder.md.tpl"`             |
| `option attribution-trailer-style none` | `options.attribution.trailer_style = "none"`        |
| `option attribution-generated-with false` | `options.attribution.generated_with = false`       |

### Shell expansion in evo.json

In JSON, only selected string fields are run through the embedded shell at load
time (in `evorc`, everything is native Bash so this table does not apply):

| Surface                                                         | Expansion                          |
| --------------------------------------------------------------- | ---------------------------------- |
| Provider `api_key`, `base_url`, `api_endpoint`, `extra_headers` | yes                                |
| Provider `extra_body`                                           | **no** (JSON passthrough)          |
| MCP `command`, `args`, `env`, `headers`, `url`                  | yes                                |
| LSP `command`, `args`, `env`                                    | yes                                |
| Hook `command`                                                  | runs via `sh -c`, not the resolver |

Supported constructs: `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:+alt}`,
`${VAR:?message}`, `$(command)`. An unset variable expands to empty; a failing
`$(command)` is a hard error. A header that resolves to empty is dropped from
the request.

### Legacy JSON compatibility

Legacy configs usually still load, but some fields are normalized on read:

- `$schema` is only for editor autocomplete. Evo stores it as a plain string and
  does not validate against it at runtime. A wrong or missing value does not
  stop config loading; point it at `./schema.json` or the project's raw schema
  URL if you want autocomplete.
- Model field `supports_attachments: true` is migrated to
  `capabilities.vision: true`. Image attachments are filtered out unless
  `capabilities.vision` is true.
- Provider type `openai` is normalized to `responses`, and `openai-compat` to
  `completions`. For ordinary OpenAI-compatible relay endpoints
  (`/v1/chat/completions`), write `"type": "completions"` explicitly.
- `permissions.allowed_tools` containing unknown tool names, or `skills_paths`
  pointing at nonexistent directories, do not fail config loading. The invalid
  tool simply has no effect and the missing skill path is treated as empty.
- A `hook` command that points at a script must be executable and exist when the
  hook fires. Put it under `~/.config/evo/hooks/` and run `chmod +x` on it.

### Security note

Both formats are trusted code. `evorc` runs entirely, and any `$(...)` in
`evo.json` runs at load time, with the invoking user's shell privileges,
before the UI appears. Don't launch Evo in a directory whose config you
haven't reviewed.

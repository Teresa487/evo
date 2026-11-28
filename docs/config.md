# evo 配置完整参考

配置文件加载顺序（后者覆盖前者，同名键深合并）：

| 优先级 | 路径 | 格式 |
|---|---|---|
| 1（最低） | `/etc/evo/evo.json` | JSON（仅 Unix） |
| 2 | `~/.config/evo/evo.json` | JSON |
| 3 | `~/.config/evo/evorc` | Bash |
| 4 | `~/.local/share/evo/evo.json` | JSON（机器写入，别手改） |
| 5 | 项目内向上查找 `.evorc` / `evorc` / `.evo.json` / `evo.json` | Bash / JSON |

同目录下 `evorc` 优先于 `evo.json`，并且会打一条 warning。

两个格式的取舍：

- **`evo.json` 是严格 JSON，不能写注释**。想写注释请用 `evorc`（Bash，`#` 即注释）。
- **`evorc` 覆盖不到所有字段**。主题、`tools.*`、模型的 `effort_levels` / `audio` / `options`、`$schema` 只能用 JSON 写。

## 一、完整 JSON（可直接使用）

下面这份把所有字段都写出来了，值是示例。用不到的段可以整段删；真正必须保留的是 provider / model 那些核心项，具体见后面的字段说明。

```json
{
  "$schema": "https://raw.githubusercontent.com/Teresa487/evo/main/schema.json",

  "providers": {
    "my-llm": {
      "id": "my-llm",
      "name": "My LLM",
      "type": "completions",
      "base_url": "https://api.example.com/v1",
      "api_key": "$MY_API_KEY",
      "disable": false,
      "flat_rate": false,
      "discover_models": true,
      "system_prompt_prefix": "You are working on a Rust codebase.",
      "extra_headers": {
        "X-Title": "evo",
        "X-Org": "$MY_ORG_ID"
      },
      "extra_body": {
        "chat_template_kwargs": {"enable_thinking": false}
      },
      "provider_options": {},
      "aws_auth_refresh": "aws sso login --profile $AWS_PROFILE",
      "models": [
        {
          "id": "gpt-4o",
          "name": "GPT-4o",
          "type": "completions",
          "pricing": {"input": 2.5, "output": 10, "cache_create": 0, "cache_hit": 0},
          "context_window": 128000,
          "default_max_tokens": 8192,
          "max_attachments": 5,
          "reasoning": {
            "thinking": "toggleable",
            "effort_levels": [
              {"value": "low", "display": "Low"},
              {"value": "high", "display": "High"}
            ],
            "default_effort_level": "low"
          },
          "capabilities": {"vision": true, "audio": false},
          "options": {
            "temperature": 0.7,
            "top_p": 0.95,
            "top_k": 40,
            "frequency_penalty": 0,
            "presence_penalty": 0,
            "provider_options": {}
          }
        }
      ]
    },

    "my-claude": {
      "type": "messages",
      "base_url": "https://api.anthropic.com",
      "api_key": "$ANTHROPIC_API_KEY",
      "models": [{"id": "claude-sonnet-5", "name": "Claude Sonnet 5"}]
    },

    "local-ollama": {
      "type": "ollama",
      "base_url": "http://localhost:11434/v1",
      "models": [{"id": "qwen3-coder", "name": "Qwen3 Coder"}]
    },

    "openai": {
      "oauth": {
        "access_token": "",
        "refresh_token": "",
        "expires_in": 3600,
        "expires_at": 0
      }
    }
  },

  "models": {
    "large": {
      "provider": "my-llm",
      "model": "gpt-4o",
      "max_tokens": 8192,
      "temperature": 0.7,
      "top_p": 0.95,
      "top_k": 40,
      "frequency_penalty": 0,
      "presence_penalty": 0,
      "reasoning_effort": "high",
      "think": false,
      "provider_options": {}
    },
    "small": {
      "provider": "my-llm",
      "model": "gpt-4o"
    }
  },

  "mcp": {
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {"Authorization": "Bearer $GITHUB_TOKEN"},
      "timeout": 30,
      "disabled": false,
      "enabled_tools": ["search_code"],
      "disabled_tools": [],
      "sessionless": true,
      "oauth": false,
      "oauth_client_id": "",
      "oauth_client_secret": "",
      "oauth_callback_port": 0,
      "channel_enabled": false,
      "channel_reply": {
        "user": {
          "tool": "send_message_to_user",
          "target_param": "user_id",
          "target_meta": "sender"
        },
        "group": {
          "tool": "send_group_message",
          "target_param": "group_id"
        },
        "message_param": "message",
        "suppress_tools": ["send"]
      }
    },
    "files": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {"NODE_NO_WARNINGS": "1"},
      "timeout": 10
    }
  },

  "lsp": {
    "gopls": {
      "command": "gopls",
      "args": [],
      "env": {},
      "filetypes": ["go", "gomod"],
      "root_markers": ["go.mod"],
      "init_options": {},
      "options": {
        "gofumpt": true,
        "staticcheck": true
      },
      "timeout": 60,
      "disabled": false
    }
  },

  "options": {
    "context_paths": [".cursorrules", "AGENTS.md", "docs/architecture.md"],
    "global_context_paths": ["~/.config/evo/AGENTS.md"],
    "skills_paths": ["~/.config/evo/skills", "./skills"],
    "disabled_skills": ["some-skill"],
    "disabled_tools": ["sourcegraph"],
    "debug": false,
    "debug_lsp": false,
    "disable_auto_summarize": false,
    "data_directory": ".evo",
    "initialize_as": "AGENTS.md",
    "auto_lsp": true,
    "progress": true,
    "notifications": "auto",
    "request_timeout": 60,
    "max_background_jobs": 50,
    "pinentry_integrated": true,
    "pinentry_cache_timeout": 0,
    "disable_metrics": false,
    "coreutils": "auto",
    "attribution": {
      "trailer_style": "assisted-by",
      "generated_with": true
    },
    "prompts": {
      "coder": "./prompts/coder.md",
      "summary": "./prompts/summary.md"
    },
    "tui": {
      "compact_mode": false,
      "diff_mode": "unified",
      "active_theme": "charmtone-panther",
      "transparent": false,
      "scrollbar": "default",
      "mouse": true,
      "nerd_fonts": true,
      "exit_banner": "default",
      "completions": {"max_depth": 10, "max_items": 100}
    },
    "experimental": {
      "client_server": false,
      "server_ready_timeout": 30,
      "server_idle_timeout": 60,
      "server_detach_grace": 10
    }
  },

  "permissions": {
    "allowed_tools": ["view", "ls", "grep", "glob"],
    "safe_commands": ["go build", "cargo check"],
    "blocked_commands": ["kubectl", "terraform"],
    "allowed_commands": ["curl"]
  },

  "tools": {
    "ls": {"max_depth": 0, "max_items": 1000},
    "grep": {"timeout": "5s"},
    "glob": {"timeout": "30s"}
  },

  "hooks": {
    "PreToolUse": [
      {
        "name": "log-bash",
        "matcher": "^bash$",
        "command": "echo \"$EVO_TOOL_INPUT_COMMAND\" >> ~/.evo-audit.log",
        "timeout": 30
      }
    ],
    "SessionStart": [
      {"command": "echo started"}
    ]
  },

  "env": {
    "AWS_PROFILE": "work",
    "NO_COLOR": "1"
  }
}
```

## 二、字段说明

「必填」= 缺了会加载失败或该条目被静默丢弃；「可选」= 省略即用默认值。必填只针对实际启用的那部分配置，例如自定义 provider、模型选择和 MCP server；不写整段配置就没有必填项。

### 顶层

| 字段 | 必填性 | 说明 |
|---|---|---|
| `$schema` | 可选 | 编辑器补全用，运行时不读 |
| `providers` | 可选 | 至少有一个可用 provider 才能跑 agent |
| `models` | 可选 | 省略则按 provider 顺序自动挑默认模型 |
| `mcp` / `lsp` | 可选 | 键是 server 名字 |
| `options` | 可选 | 应用行为，全部有默认值 |
| `permissions` | 可选 | 权限策略 |
| `tools` | 可选 | 内置工具的超参 |
| `hooks` | 可选 | 键必须是合法事件名，值是该事件的 hook 数组 |
| `env` | 可选 | 启动时注入的环境变量，provider 的 `$VAR` 从这里解析 |

### `providers.<id>`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `id` | 可选 | 键名 | 一般不用写 |
| `name` | 可选 | `id` | 显示名 |
| `type` | 可选 | `completions` | `completions`（OpenAI Chat） / `responses`（OpenAI Responses） / `messages`（Anthropic） / `hyper` / `ollama` / `litellm` / `llamacpp` / `lmstudio` / `omlx` |
| `base_url` | **自定义 provider 必填** | — | 缺失或解析为空 → 整个 provider 被丢弃 |
| `api_key` | 登录类可选，其余建议填 | — | 支持 `$VAR` 和 `$(cmd)` |
| `disable` | 可选 | `false` | 置 true 等同于删除 |
| `flat_rate` | 可选 | `false` | 订阅/包月计费，不累计费用 |
| `discover_models` | 可选 | `true` | 走 `/v1/models` 自动发现；与手写 models 合并，手写的优先 |
| `system_prompt_prefix` | 可选 | — | 追加到该 provider 的 system prompt 前 |
| `extra_headers` | 可选 | — | 支持 `$VAR` / `$(cmd)`；解析为空串的头会被丢弃 |
| `extra_body` | 可选 | — | 原样合并进请求体，**不做**变量展开 |
| `provider_options` | 可选 | — | provider 专属透传 |
| `aws_auth_refresh` | 可选 | — | Bedrock 凭据过期时执行的命令 |
| `models` | **必填（非目录内 provider）** | — | 空数组 + 发现失败 → provider 被丢弃 |
| `oauth` | 可选 | — | OAuth 登录后由 `evo login` 写入，别手填 |

### `providers.<id>.models[]`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `id` | **必填** | — | provider API 里的模型 ID |
| `name` | 可选 | `id` | 显示名 |
| `type` | 可选 | 继承 provider | 单模型覆盖 wire 协议 |
| `pricing.input` / `.output` | 可选 | `0` | 美元 / 百万 token |
| `pricing.cache_create` / `.cache_hit` | 可选 | `0` | 缓存写/读单价 |
| `context_window` | 可选 | `0` | 上下文上限 |
| `default_max_tokens` | 可选 | `0` | 默认输出上限 |
| `max_attachments` | 可选 | `0` | 单次最多附件数 |
| `reasoning.thinking` | 可选 | `never` | `always` / `never` / `toggleable` |
| `reasoning.effort_levels` | 可选 | — | `[{value, display}]` 列表 |
| `reasoning.default_effort_level` | 可选 | — | 默认档位 |
| `capabilities.vision` / `.audio` | 可选 | `false` | 是否支持图片/音频输入 |
| `options.*` | 可选 | — | temperature / top_p / top_k / frequency_penalty / presence_penalty / provider_options |

> schema 把 `name` / `pricing` / `context_window` / `default_max_tokens` / `reasoning` / `capabilities` 都标成 required，但**运行时只校验 `id`**，其余缺了就用零值。编辑器会标红，可以忽略。

### `models.large` / `models.small`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `provider` | **必填** | — | 必须是 `providers` 下的键 |
| `model` | **必填** | — | 必须是该 provider 的模型 ID |
| `max_tokens` | 可选 | 模型默认 | 覆盖输出上限 |
| `temperature` / `top_p` / `top_k` | 可选 | 模型默认 | 采样参数 |
| `frequency_penalty` / `presence_penalty` | 可选 | 模型默认 | 重复惩罚 |
| `reasoning_effort` | 可选 | 模型默认 | `low` / `medium` / `high` |
| `think` | 可选 | `false` | 强制开思考（Anthropic） |
| `provider_options` | 可选 | — | 模型级透传 |

`small` 省略时：provider 在目录里取它的默认小模型，否则直接复用 `large`。

### `mcp.<name>`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `type` | 可选（JSON 里建议写） | `stdio` | `stdio` / `sse` / `http`；未知值 → server 启动失败 |
| `command` | **stdio 必填** | — | 空则报 "requires a non-empty 'command' field" |
| `args` | 可选 | — | 支持变量展开 |
| `env` | 可选 | — | 启动该进程的环境变量 |
| `url` | **sse / http 必填** | — | 空则报错 |
| `headers` | 可选 | — | 支持 `$VAR` / `$(cmd)`，空值丢弃 |
| `timeout` | 可选 | `10` | 连接超时（秒） |
| `disabled` | 可选 | `false` | 停用该 server |
| `enabled_tools` | 可选 | 全部 | 白名单 |
| `disabled_tools` | 可选 | 无 | 黑名单 |
| `sessionless` | 可选 | 自动识别 | 不维护 MCP session 的 server（GitHub 等）置 true，跳过 subscriptions 流 |
| `oauth` | 可选 | `false` | 仅 `type: http`，走 OAuth 2.1 |
| `oauth_client_id` / `oauth_client_secret` | 可选 | — | 提供方不支持动态注册时填 |
| `oauth_callback_port` | 可选 | 自动挑 | 需要固定回调端口时填 |
| `channel_enabled` | 可选 | `false` | 把该 MCP 当渠道（等价 `--channels`） |
| `channel_reply` | 可选 | — | 自动把回复路由回渠道 |

### `lsp.<name>`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `command` | 可选 | 从内置目录补 | 名字命中 gopls / pylsp 等时可省 |
| `args` / `env` | 可选 | — | 支持变量展开 |
| `filetypes` | 可选 | 内置目录 | 该 server 负责的文件类型 |
| `root_markers` | 可选 | 内置目录 | 判断项目根 |
| `init_options` | 可选 | 内置目录 | LSP `initialize` 的入参 |
| `options` | 可选 | 内置目录 | server 专属设置 |
| `timeout` | 可选 | `30` | 初始化超时（秒） |
| `disabled` | 可选 | `false` | 停用 |

### `options`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `context_paths` | 可选 | 一批约定文件 | 额外喂给模型的上下文文件 |
| `global_context_paths` | 可选 | `~/.config/evo/EVO.md`、`~/.config/AGENTS.md` | 全局上下文 |
| `skills_paths` | 可选 | 见下 | skills 目录；内置的 `~/.config/evo/skills`、`<项目>/skills` 总会加上 |
| `disabled_skills` | 可选 | — | 按名字停用 skill |
| `disabled_tools` | 可选 | — | 隐藏给模型的工具 |
| `debug` | 可选 | `false` | Debug 级别日志（等价 `-d`） |
| `debug_lsp` | 可选 | `false` | LSP 详细日志 |
| `disable_auto_summarize` | 可选 | `false` | 关掉自动摘要 |
| `data_directory` | 可选 | `.evo` | 相对路径按工作目录解析 |
| `initialize_as` | 可选 | `AGENTS.md` | 项目初始化写哪个文件 |
| `auto_lsp` | 可选 | `true` | 按 root marker 自动起 LSP |
| `progress` | 可选 | `true` | 长操作的进度指示 |
| `notifications` | 可选 | `auto` | `auto` / `native` / `osc` / `bell` / `disabled` |
| `request_timeout` | 可选 | `60` | 单次请求秒数；0 关闭；按上下文规模放大，超时会重试（主对话 10 次、后台 3 次） |
| `max_background_jobs` | 可选 | `50` | 并发后台 shell 上限 |
| `pinentry_integrated` | 可选 | `true` | GPG 密码用 evo 自己的弹窗 |
| `pinentry_cache_timeout` | 可选 | `0` | 密码在内存里留多久 |
| `disable_metrics` | 可选 | `false` | 关闭统计上报 |
| `coreutils` | 可选 | `auto` | `turtle` / `system` / `auto` / `off` |
| `attribution` | 可选 | `assisted-by` + `generated_with: true` | 提交信息署名 |
| `prompts` | 可选 | — | 用文件替换内置 prompt，键见下表 |
| `tui` | 可选 | — | 见下 |
| `experimental` | 可选 | — | 见下 |

`prompts` 可替换的键：`coder`、`task`、`plan`、`summary`、`title`、`initialize`。替换是**整体替换**，内置模板注入的 skills / 上下文文件 / LSP 状态都会丢，除非你新模板自己带上。

`options.tui`：

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `compact_mode` | 可选 | `false` | 紧凑布局 |
| `diff_mode` | 可选 | `unified` | `unified` / `split` |
| `active_theme` | 可选 | `charmtone-panther` | 主题名 |
| `transparent` | 可选 | `false` | 透明背景 |
| `scrollbar` | 可选 | `default` | `default`（2 秒后隐藏） / `always` / `never` |
| `mouse` | 可选 | `true` | **置 false 交给终端处理选中和复制粘贴** |
| `nerd_fonts` | 可选 | 自动探测 | `true` / `false` |
| `exit_banner` | 可选 | `default` | `default` / `compact` / `none` |
| `completions.max_depth` / `.max_items` | 可选 | `0` / `0`（=内置上限） | 补全扫描范围 |

`options.experimental`（都不稳定，环境变量优先）：

| 字段 | 必填性 | 默认 | 对应的环境变量 |
|---|---|---|---|
| `client_server` | 可选 | `false` | `EVO_CLIENT_SERVER` |
| `server_ready_timeout` | 可选 | — | `EVO_SERVER_READY_TIMEOUT` |
| `server_idle_timeout` | 可选 | `60` | `EVO_SERVER_IDLE_TIMEOUT` |
| `server_detach_grace` | 可选 | `10` | `EVO_SERVER_DETACH_GRACE` |

### `permissions`

| 字段 | 必填性 | 说明 |
|---|---|---|
| `allowed_tools` | 可选 | 免确认的工具名 |
| `safe_commands` | 可选 | 精确匹配的完整命令行，免确认。**多一个参数就不匹配** |
| `blocked_commands` | 可选 | 额外列入危险命令，普通模式会弹确认 |
| `allowed_commands` | 可选 | 从危险列表移除（含内置项），比如放行 `curl` |

### `tools`

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `ls.max_depth` | 可选 | `0`（不限） | ls 深度 |
| `ls.max_items` | 可选 | `1000` | ls 条目上限 |
| `grep.timeout` | 可选 | `5s` | grep 超时 |
| `glob.timeout` | 可选 | `30s` | glob 超时 |

### `hooks.<Event>[]`

`hooks` 整段可选；只有实际写了某条 hook 时，下面这些字段才有意义：

| 字段 | 必填性 | 默认 | 说明 |
|---|---|---|---|
| `command` | 写了这条 hook 时必填 | — | 缺失直接报错；空 hook 段本身不用写 |
| `name` | 可选 | `command` | 显示名；**要单独删除必须写** |
| `matcher` | 可选 | 匹配全部 | 对工具名匹配的正则 |
| `timeout` | 可选 | `30` | 秒 |

合法事件名（大小写和 `_` 会被折叠，`pretooluse` 也认）：

`PreToolUse`、`PostToolUse`、`PostToolUseFailure`、`PermissionRequest`、`PermissionResult`、`SessionStart`、`SessionEnd`、`TurnStart`、`TurnEnd`、`StopFailure`、`Interrupt`、`PreCompact`、`PostCompact`。

hook 脚本能拿到的环境变量（前缀是 `EVO_`）：`EVO_EVENT`、`EVO_SESSION_ID`、`EVO_CWD`、`EVO_PROJECT_DIR`（另有 `CLAUDE_PROJECT_DIR` 同值）、`EVO_TOOL_NAME`、`EVO_TOOL_INPUT_COMMAND`、`EVO_TOOL_INPUT_FILE_PATH`。

hook 的 stdout 若是 JSON，可以干预流程：

```json
{"decision": "allow", "reason": "", "halt": false, "context": "附加给模型的上下文", "updated_input": {}, "updated_output": {}}
```

`decision` 可取 `allow` / `deny` / `block`；`continue: false` 等价 `halt: true`。空输出或非 JSON 输出都视为「不干预」。也认 Claude Code 的 `hookSpecificOutput` 形式。

## 三、等价 evorc（带注释，可读性更好）

```bash
#!/usr/bin/env bash
# evo 的 Bash 配置。放在 ~/.config/evo/evorc 或项目里的 evorc。
# 只有 provider / model / mcp / lsp / hook / permissions / option 这些
# 内置命令能表达的字段可以用它写；主题、tools.*、模型的 effort_levels
# 等仍然只能写 JSON。

# ---- provider ----
# --base-url 对非目录内的 provider 是必填的，缺了会被丢弃。
provider add my-llm \
  --name "My LLM" \
  --type completions \
  --base-url "https://api.example.com/v1" \
  --api-key "$MY_API_KEY" \
  --extra-header X-Title "evo" \
  --extra-body '{"chat_template_kwargs":{"enable_thinking":false}}' \
  --system-prompt-prefix "You are working on a Rust codebase."

# 本地 ollama，不需要 api-key
provider add local-ollama --type ollama --base-url "http://localhost:11434/v1"

# ---- 模型 ----
# 非目录内的 provider，models 必须至少有一条，否则整个 provider 被丢弃。
model add my-llm/gpt-4o \
  --name "GPT-4o" \
  --context-window 128000 \
  --default-max-tokens 8192 \
  --thinking toggleable \
  --reasoning-effort low \
  --supports-images true \
  --price-input 2.5 \
  --price-output 10 \
  --max-attachments 5

model add local-ollama/qwen3-coder --name "Qwen3 Coder" --context-window 262144

# 选模型：provider/model 必填，其后都是可选覆盖
model large my-llm/gpt-4o --max-tokens 8192 --temperature 0.7 --top-p 0.95
model small my-llm/gpt-4o

# ---- MCP ----
# stdio（默认 type），--command 必填
mcp add files --command npx \
  --args -y --args @modelcontextprotocol/server-filesystem --args /tmp \
  --timeout 10

# http，--url 必填
mcp add github --type http --url "https://api.githubcopilot.com/mcp/" \
  --header Authorization "Bearer $GITHUB_TOKEN" \
  --sessionless true \
  --enabled-tools search_code

# ---- LSP ----
# 名字命中内置目录时可省 --command
lsp add gopls --command gopls --filetypes go --root-markers go.mod --timeout 60

# ---- 权限 ----
permissions allow view ls grep glob
permissions safe "go build" "cargo check"
permissions block kubectl terraform
permissions unblock curl

# ---- hook ----
# PreToolUse 记得配 --matcher 收窄范围，--command 必填
hook add PreToolUse --name log-bash --matcher '^bash$' \
  --command 'echo "$EVO_TOOL_INPUT_COMMAND" >> ~/.evo-audit.log' --timeout 30

# ---- option ----
option data-directory .evo
option request-timeout 60
option max-background-jobs 50
option notifications auto
option initialize-as AGENTS.md
option coreutils auto
option metrics false              # 注意：这是「关闭指标」，存的键是 disable_metrics
option auto-summarize true
option context-path docs/architecture.md
option skill-path ./skills
option disable-skill some-skill
option prompt coder ./prompts/coder.md
option attribution-trailer-style assisted-by
option attribution-generated-with true

# 列表项可以用 reset 清空后再加
option reset skill-path
option skill-path ./skills

# ---- TUI ----
option ui compact false
option ui diff unified
option ui transparent false
option ui mouse true        # 置 false 让终端自己处理选中/粘贴
option ui nerd-fonts true
option ui scrollbar default
option ui exit-banner default
option ui completions-max-depth 10
option ui completions-max-items 100

# ---- experimental ----
option experimental client-server false
option experimental server-idle-timeout 60
option experimental server-detach-grace 10
```

## 四、注意事项

1. **`evo.json` 里写注释会直接解析失败**，`loadFromConfigPaths` 用 `json.Valid` 卡得很死。要注释只能用 `evorc`。
2. **自定义 provider 缺 `base_url` 或 `models` 会被静默删除**，日志里只有一条 `Skipping custom provider due to missing API endpoint`。
3. `option provider-auto-update` 和 `option default-providers` 是历史残留：对应的配置字段已经删了，写了不报错也不生效。
4. `evo` 对配置**不做运行时 schema 校验**，schema 只用于编辑器提示，所以字段名拼错可能静默失效。
5. 环境变量覆盖：`EVO_GLOBAL_CONFIG` / `EVO_GLOBAL_DATA` / `EVO_DATA_DIR` / `EVO_CACHE_DIR` / `EVO_SKILLS_DIR`，以及 `EVO_CLIENT_SERVER` / `EVO_SERVER_READY_TIMEOUT` / `EVO_SERVER_IDLE_TIMEOUT` / `EVO_SERVER_DETACH_GRACE`。
6. `$schema` 只给编辑器补全用，运行时只把它当普通字符串保存。写成 `"evo.json"` 不影响启动，但编辑器可能找不到 schema；想补全就写成本仓库的 `./schema.json` 或对应的 raw URL。
7. 从旧配置迁移过来的字段通常还能跑，但建议改成新写法：模型的 `supports_attachments: true` 会自动迁到 `capabilities.vision: true`；`capabilities` 里没有 `vision: true` 时，图片附件会被过滤掉。
8. provider 的旧类型也有兼容映射：`openai` 会按 `responses` 处理，`openai-compat` 会按 `completions` 处理。普通 OpenAI Chat 中转站（`/v1/chat/completions`）建议显式写 `"type": "completions"`。
9. `hook` 的 `command` 指向脚本时，脚本必须存在且可执行；放到 `~/.config/evo/hooks/` 下时记得 `chmod +x`。路径不存在或不可执行会在 hook 触发时失败，不影响配置加载。
10. `permissions.allowed_tools`、`skills_paths` 里写了不存在的工具名或目录不会让配置加载失败：无效工具名不会生效，不存在的 skill 目录会按空目录处理。先能跑可以先不改，但排查“为什么没生效”时要先看这些值。

# evo 配置说明

配置文件按顺序加载，后者覆盖前者（同名键深合并）：

```
/etc/evo/evo.json            系统级，仅 Unix
~/.config/evo/evo.json       用户全局
~/.config/evo/evorc          用户全局，Bash 格式（可写注释）
~/.local/share/evo/evo.json  机器写入，别手改
<项目>/.evorc 或 evorc 或 .evo.json 或 evo.json
```

同目录下 `evorc` 优先于 `evo.json`。`evo.json` 是严格 JSON，**不能写注释**，所以注释都写在下面这份里。

只有一个约定：标了**【必填】**的字段不能删，其余都是可选的，删掉就用默认值。

```jsonc
{
  // 编辑器补全用，运行时不读
  "$schema": "https://raw.githubusercontent.com/Teresa487/evo/main/schema.json",

  // ==========================================================================
  // providers：AI 供应商。至少得有一个可用的才能跑。
  // 只认配置文件里手写的定义，例外是 openai / xai / copilot / hyper
  // 这四个可登录的，它们带内置目录。
  // ==========================================================================
  "providers": {
    "my-llm": {
      "id": "my-llm",           // 一般不用写，默认等于键名
      "name": "My LLM",         // 显示名，默认等于键名

      // completions（OpenAI Chat，中转站基本都是这个）/ responses /
      // messages（Anthropic）/ hyper / ollama / litellm / llamacpp /
      // lmstudio / omlx。默认 completions
      "type": "completions",

      // 【必填】自定义 provider 必须写。缺失或解析为空 → 整个 provider 被丢弃
      "base_url": "https://api.example.com/v1",

      // 支持 $VAR 和 $(cmd)。登录类 provider 可以用 oauth 代替
      "api_key": "$MY_API_KEY",

      "disable": false,          // 置 true 等同于删除
      "flat_rate": false,        // 订阅/包月，不计费用
      "discover_models": true,   // 走 /v1/models 自动发现，和手写的合并，手写的优先

      "system_prompt_prefix": "You are working on a Rust codebase.",

      // 每个请求都带的头，支持变量展开；解析成空串的头会被丢弃
      "extra_headers": {
        "X-Title": "evo",
        "X-Org": "$MY_ORG_ID"
      },

      // 原样合并进请求体，这里不支持变量展开
      "extra_body": {
        "chat_template_kwargs": { "enable_thinking": false }
      },

      "provider_options": {},

      // Bedrock 凭据过期时执行，只有 Bedrock 用得上
      "aws_auth_refresh": "aws sso login --profile $AWS_PROFILE",

      // 【必填】非目录内 provider 必须至少列一条
      "models": [
        {
          "id": "gpt-4o",        // 【必填】provider API 里的真实模型 ID
          "name": "GPT-4o",      // 默认等于 id
          "type": "completions", // 默认继承 provider 的 type

          "pricing": {           // 美元 / 百万 token
            "input": 2.5,
            "output": 10,
            "cache_create": 0,
            "cache_hit": 0
          },

          "context_window": 128000,
          "default_max_tokens": 8192,
          "max_attachments": 5,  // 单次最多几个附件

          "reasoning": {
            "thinking": "toggleable",     // always / never（默认）/ toggleable
            "effort_levels": [
              { "value": "low", "display": "Low" },
              { "value": "high", "display": "High" }
            ],
            "default_effort_level": "low"
          },

          "capabilities": {      // 决定能不能收图片/音频附件，默认都 false
            "vision": true,
            "audio": false
          },

          "options": {           // 模型级采样参数
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

    // 走 Anthropic 协议
    "my-claude": {
      "type": "messages",
      "base_url": "https://api.anthropic.com",   // 【必填】
      "api_key": "$ANTHROPIC_API_KEY",
      "models": [{ "id": "claude-sonnet-5", "name": "Claude Sonnet 5" }]  // 【必填】
    },

    // 本地 ollama，不需要 api_key
    "local-ollama": {
      "type": "ollama",
      "base_url": "http://localhost:11434/v1",   // 【必填】
      "models": [{ "id": "qwen3-coder", "name": "Qwen3 Coder" }]  // 【必填】
    },

    // 这一段由 `evo login openai` 自动写入，不要手填。
    // 端点和模型目录来自内置目录，不需要 base_url / models
    "openai": {
      "oauth": {
        "access_token": "",
        "refresh_token": "",
        "expires_in": 3600,
        "expires_at": 0
      }
    }
  },

  // ==========================================================================
  // models：选哪个模型。只有 large（主力）和 small（小任务/后台）两个槽。
  // 整段省略时会按 provider 顺序自动挑默认模型。
  // ==========================================================================
  "models": {
    "large": {
      "provider": "my-llm",  // 【必填】必须是 providers 里的键
      "model": "gpt-4o",     // 【必填】必须是该 provider 的模型 ID

      "max_tokens": 8192,    // 覆盖模型默认输出上限
      "temperature": 0.7,
      "top_p": 0.95,
      "top_k": 40,
      "frequency_penalty": 0,
      "presence_penalty": 0,
      "reasoning_effort": "high",  // low / medium / high
      "think": false,              // 强制开思考（Anthropic 系）
      "provider_options": {}
    },
    // 省略时：provider 在目录里就用它的默认小模型，否则直接复用 large
    "small": {
      "provider": "my-llm",
      "model": "gpt-4o"
    }
  },

  // ==========================================================================
  // mcp：MCP 服务器。键是服务器名字。
  // ==========================================================================
  "mcp": {
    "github": {
      "type": "http",            // 【必填】stdio / sse / http
      "url": "https://api.githubcopilot.com/mcp/",   // 【必填，sse/http】

      "headers": { "Authorization": "Bearer $GITHUB_TOKEN" },

      "timeout": 30,             // 默认 10，秒
      "disabled": false,

      "enabled_tools": ["search_code"],  // 白名单
      "disabled_tools": [],              // 黑名单

      // 不维护 MCP session 的服务器（GitHub MCP 等）置 true，
      // 否则会被 404 拒绝。省略时自动识别已知的
      "sessionless": true,

      "oauth": false,            // 仅 type=http，走 OAuth 2.1
      "oauth_client_id": "",     // 提供方不支持动态注册时才填
      "oauth_client_secret": "",
      "oauth_callback_port": 0,  // 省略则自动挑空闲端口

      // 把这个 MCP 当渠道用（等价 --channels）
      "channel_enabled": false,

      // 自动把回复路由回渠道
      "channel_reply": {
        "user": {                        // 私聊回复走哪个工具
          "tool": "send_message_to_user",
          "target_param": "user_id",     // 这个工具收「发给谁」的参数名
          "target_meta": "sender"
        },
        "group": {                       // 群聊回复
          "tool": "send_group_message",
          "target_param": "group_id"
        },
        "message_param": "message",      // 接收正文的参数名，默认 message
        "suppress_tools": ["send"]       // 模型已调过这些工具就不再补发
      }
    },

    "files": {
      "type": "stdio",           // 建议写明；用 mcp add 时自动填 stdio
      "command": "npx",          // 【必填，stdio】
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": { "NODE_NO_WARNINGS": "1" },
      "timeout": 10
    }
  },

  // ==========================================================================
  // lsp：语言服务器。键是服务器名字。
  // ==========================================================================
  "lsp": {
    "gopls": {
      // 名字能命中内置目录（gopls / pylsp / typescript-language-server 等）
      // 时可以省略，会自动补
      "command": "gopls",

      "args": [],
      "env": {},

      "filetypes": ["go", "gomod"],   // 省略时从内置目录补
      "root_markers": ["go.mod"],

      "init_options": {},             // LSP initialize 请求的入参
      "options": {                    // 服务器专属设置，原样传给 LSP
        "gofumpt": true,
        "staticcheck": true
      },

      "timeout": 60,                  // 默认 30，秒
      "disabled": false
    }
  },

  // ==========================================================================
  // options：应用行为。以下全部有默认值。
  // ==========================================================================
  "options": {
    // 额外喂给模型的上下文文件，追加到内置默认列表之后
    "context_paths": [".cursorrules", "AGENTS.md", "docs/architecture.md"],

    // 默认 ~/.config/evo/CRUSH.md 和 ~/.config/AGENTS.md
    "global_context_paths": ["~/.config/evo/AGENTS.md"],

    // 内置的 ~/.config/evo/skills 和 <项目>/skills 总会加上
    "skills_paths": ["~/.config/evo/skills", "./skills"],

    "disabled_skills": ["some-skill"],   // 按名字停用 skill
    "disabled_tools": ["sourcegraph"],   // 隐藏给模型的工具

    "debug": false,                // 开 Debug 日志（等价 -d）
    "debug_lsp": false,            // LSP 详细日志
    "disable_auto_summarize": false,

    "data_directory": ".evo",      // 放 SQLite 和 logs/，相对路径按工作目录解析
    "initialize_as": "AGENTS.md",  // 项目初始化写哪个文件
    "auto_lsp": true,              // 按 root_markers 自动起 LSP
    "progress": true,              // 长操作显示进度

    // auto / native / osc / bell / disabled。
    // auto 会按环境挑：本地用系统通知，SSH 用 OSC
    "notifications": "auto",

    // 单次 LLM 请求的秒数，默认 60。0 表示不限。
    // 会按上下文规模自动放大，超时会重试。
    // 流式响应只在两个 token 之间沉默这么久才中断
    "request_timeout": 60,

    "max_background_jobs": 50,     // 同时能跑几个后台 shell 任务

    "pinentry_integrated": true,   // GPG 密码用 evo 自己的弹窗
    "pinentry_cache_timeout": 0,   // 密码在内存里留几秒，0 不留

    "disable_metrics": false,      // 关掉统计上报

    // shell 里的 coreutils（ls/cat/grep 等）由谁提供：
    // turtle / system / auto（默认）/ off
    "coreutils": "auto",

    // 提交信息署名
    "attribution": {
      "trailer_style": "assisted-by",  // none / co-authored-by / assisted-by
      "generated_with": true
    },

    // 用文件整体替换内置 prompt。
    // 可替换的键：coder / task / plan / summary / title / initialize。
    // 注意是整体替换，内置模板注入的 skills、上下文文件、LSP 状态都会丢，
    // 除非你的新模板自己带上
    "prompts": {
      "coder": "./prompts/coder.md",
      "summary": "./prompts/summary.md"
    },

    "tui": {
      "compact_mode": false,      // 紧凑布局
      "diff_mode": "unified",     // unified 行内 / split 左右对照
      "active_theme": "charmtone-panther",
      "transparent": false,

      // default 两秒后自动隐藏 / always / never
      "scrollbar": "default",

      // 置 false 会把选中和复制粘贴交还给终端。
      // ctrl+v 或鼠标选中不顺手时先试这个
      "mouse": true,

      "nerd_fonts": true,         // 省略则自动探测
      "exit_banner": "default",   // default 完整 logo / compact 精简 / none 不打

      // 路径补全的扫描范围，0 表示用内置上限
      "completions": {
        "max_depth": 10,
        "max_items": 100
      }
    },

    // 未完成的实验性功能，随时可能变。环境变量优先于这里
    "experimental": {
      "client_server": false,       // CRUSH_CLIENT_SERVER
      "server_ready_timeout": 30,   // CRUSH_SERVER_READY_TIMEOUT
      "server_idle_timeout": 60,    // CRUSH_SERVER_IDLE_TIMEOUT
      "server_detach_grace": 10     // CRUSH_SERVER_DETACH_GRACE
    }
  },

  // ==========================================================================
  // permissions：权限
  // ==========================================================================
  "permissions": {
    "allowed_tools": ["view", "ls", "grep", "glob"],  // 免确认的工具

    // 精确匹配的完整命令行，免确认。
    // 字面匹配：写了 "go build" 不覆盖 "go build -o out"
    "safe_commands": ["go build", "cargo check"],

    "blocked_commands": ["kubectl", "terraform"],  // 额外列为危险，会弹确认
    "allowed_commands": ["curl"]                   // 从危险列表移除（含内置项）
  },

  // ==========================================================================
  // tools：内置工具参数
  // ==========================================================================
  "tools": {
    "ls": {
      "max_depth": 0,     // 0 表示不限
      "max_items": 1000
    },
    "grep": { "timeout": "5s" },    // 默认 5s
    "glob": { "timeout": "30s" }    // 默认 30s，注意是带单位的字符串
  },

  // ==========================================================================
  // hooks：钩子。键必须是合法事件名（大小写和下划线会被折叠）。
  //
  //   PreToolUse         工具执行前，可 allow / deny / 改写入参
  //   PostToolUse        工具成功后，可改写输出
  //   PostToolUseFailure 工具失败后，可补上下文
  //   PermissionRequest  弹权限确认时
  //   PermissionResult   权限结果确定时
  //   SessionStart       会话创建或恢复
  //   SessionEnd         退出时
  //   TurnStart          一轮开始
  //   TurnEnd            一轮结束
  //   StopFailure        一轮因 provider 错误结束
  //   Interrupt          用户取消
  //   PreCompact         压缩上下文前，可阻止
  //   PostCompact        压缩完成后
  // ==========================================================================
  "hooks": {
    "PreToolUse": [
      {
        "name": "log-bash",     // 默认等于 command。想单独删这条必须写 name

        // 对工具名跑的正则。省略则匹配全部
        "matcher": "^bash$",

        // 【必填】缺失直接报错。脚本能拿到的环境变量（前缀是 CRUSH_）：
        //   CRUSH_EVENT / CRUSH_SESSION_ID / CRUSH_CWD / CRUSH_PROJECT_DIR
        //   CRUSH_TOOL_NAME / CRUSH_TOOL_INPUT_COMMAND / CRUSH_TOOL_INPUT_FILE_PATH
        "command": "echo \"$CRUSH_TOOL_INPUT_COMMAND\" >> ~/.evo-audit.log",

        "timeout": 30    // 默认 30 秒
      }
    ],

    "SessionStart": [
      { "command": "echo started" }   // 最简只需要 command
    ]
  },

  // hook 的 stdout 如果是 JSON 可以干预流程，例如：
  //   {"decision": "deny", "reason": "不要碰生产库", "halt": false}
  // decision 可取 allow / deny / block；continue:false 等价 halt:true。
  // 空输出或非 JSON 都视为「不干预」

  // ==========================================================================
  // env：启动时注入的环境变量。上面所有 $VAR 都从这里解析
  // ==========================================================================
  "env": {
    "AWS_PROFILE": "work",
    "NO_COLOR": "1"
  }
}
```

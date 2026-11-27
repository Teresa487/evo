package sshaskpass

import (
	"os"
	"os/user"
	"regexp"
	"strings"
)

// ClassifyPrompt maps OpenSSH askpass 提示词 到 credential kind
// 和 short key/account hint 为 dialog. Passive security key
// touches become KindTouch (surfaced 作为 warning 和 confirmed
// automatically); genuine 决定 questions (主机 key acceptance,
// ssh-add key use) become KindConfirm; everything else 是 treated 作为
// secret 提示词.
func ClassifyPrompt(prompt string) (kind Kind, keyInfo string) {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case isTouchPrompt(lower):
		return KindTouch, ""
	case isConfirmPrompt(lower):
		return KindConfirm, ""
	default:
		return KindPassword, keyInfoFromPrompt(prompt)
	}
}

// ParsePrompt classifies OpenSSH askpass 提示词 和 fills 在
// friendly copy Crush shows 在 place 的 OpenSSH's terse phrasing:
// short 标题 为 dialog heading 和 human-readable description
// 的 what 是 being asked 为. raw 提示词 是 preserved 在 结果.
func ParsePrompt(prompt string) PromptRequest {
	kind, keyInfo := ClassifyPrompt(prompt)
	lower := strings.ToLower(prompt)

	// ssh-add asks "Allow use 的 key ...?" 用 key 路径 inline,
	// which ClassifyPrompt leaves out because confirmations never hide
	// 账户 behind it.
	if kind == KindConfirm && strings.Contains(lower, "allow use of key") {
		keyInfo = keyInfoFromPrompt(prompt)
	}

	style := classifyStyle(lower, kind)
	return PromptRequest{
		Prompt:  prompt,
		Kind:    kind,
		KeyInfo: keyInfo,
		Title:   style.title(),
		Message: style.message(prompt, keyInfo),
	}
}

// KeyInfoLabel returns label 为 parsed account/key hint: "Key"
// 为 key-related prompts (passphrases, security keys), "Account" 为
// password prompts.
func KeyInfoLabel(prompt string, kind Kind) string {
	if classifyStyle(strings.ToLower(prompt), kind) == stylePassword {
		return "Account"
	}
	return "Key"
}

// Describe returns copy 的 req 用 friendly copy filled 在,
// parsing raw OpenSSH 提示词 当 请求 was built 在-process
// rather than 通过 askpass subprocess.
func Describe(req PromptRequest) PromptRequest {
	parsed := ParsePrompt(req.Prompt)
	if req.Kind == "" {
		req.Kind = parsed.Kind
	}
	if req.KeyInfo == "" {
		req.KeyInfo = parsed.KeyInfo
	}
	if req.Title == "" {
		req.Title = parsed.Title
	}
	if req.Message == "" {
		req.Message = parsed.Message
	}
	return req
}

// promptStyle 是 finer-grained shape 的 提示词, used 到 pick
// 标题, 消息, 和 aside 那 fit moment. Kind alone cannot tell
// key passphrase apart 从 account password.
type promptStyle int

const (
	stylePassword promptStyle = iota
	stylePassphrase
	stylePIN
	styleHostKey
	styleKeyUse
	styleTouch
)

// classifyStyle narrows 提示词 到 它的 style 从 lowercased raw
// 文本 和 coarse credential kind.
func classifyStyle(lower string, kind Kind) promptStyle {
	switch {
	case kind == KindTouch:
		return styleTouch
	case kind == KindConfirm:
		if strings.Contains(lower, "allow use of key") {
			return styleKeyUse
		}
		return styleHostKey
	case strings.Contains(lower, "passphrase"):
		return stylePassphrase
	case pinWord.MatchString(lower):
		return stylePIN
	default:
		return stylePassword
	}
}

var pinWord = regexp.MustCompile(`\bpin\b`)

// tidy strips trailing colon (和 spaces) OpenSSH leaves 在 提示词
// 文本 和 abbreviates 用户's home 目录 到 ~.
func tidy(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), ": ")
	home, _ := os.UserHomeDir()
	if u, err := user.Current(); err == nil {
		for _, base := range []string{"/Users/", "/home/"} {
			s = replacePrefixed(s, base+u.Username)
		}
	}
	return replacePrefixed(s, home)
}

// replacePrefixed replaces dir 用 ~ wherever it starts 路径.
func replacePrefixed(s, dir string) string {
	if dir == "" || dir == "/" {
		return s
	}
	re := regexp.MustCompile(`(^|[\s'"])` + regexp.QuoteMeta(dir) + `(/|$|[\s'"])`)
	return re.ReplaceAllString(s, "${1}~${2}")
}

// 标题 是 short dialog heading 为 提示词 style.
func (s promptStyle) title() string {
	switch s {
	case styleTouch:
		return "Touch Your Security Key"
	case styleHostKey:
		return "New Host"
	case styleKeyUse:
		return "Use SSH Key"
	case stylePassphrase:
		return "SSH Passphrase"
	case stylePIN:
		return "Security Key PIN"
	default:
		return "SSH Password"
	}
}

// 消息 describes what OpenSSH 是 asking 为, 在 friendly terms.
// Phrasing Crush does 不 recognize falls back 到 raw 提示词 文本.
func (s promptStyle) message(prompt, keyInfo string) string {
	switch s {
	case styleTouch:
		return "Tap your security key to continue."
	case styleHostKey:
		return "OpenSSH has not seen this host before. Trust its key?"
	case styleKeyUse:
		if keyInfo != "" {
			return "Something wants to use the SSH key at " + keyInfo + "."
		}
		return "Something wants to use one of your SSH keys."
	case stylePassphrase:
		if keyInfo != "" {
			return "Unlock the SSH key at " + keyInfo + "."
		}
		return "Unlock your SSH key."
	case stylePIN:
		return "Please enter the PIN for your security key:"
	default:
		if keyInfo != "" {
			return "OpenSSH needs the password for " + keyInfo + "."
		}
		if trimmed := tidy(prompt); trimmed != "" {
			if !strings.HasSuffix(trimmed, "?") && !strings.HasSuffix(trimmed, ".") {
				trimmed += "."
			}
			return trimmed
		}
		return "OpenSSH needs your input."
	}
}

// isTouchPrompt reports whether 提示词 是 passive security key
// touch instruction (FIDO 用户 presence). physical touch 是
// approval, 所以 these never need decision 从 用户.
func isTouchPrompt(lower string) bool {
	for _, m := range []string{
		"confirm user presence",
		"touch the device",
		"touch your security key",
		"touch the key",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// isConfirmPrompt reports whether 提示词 是 decision question
// whose answer must come 从 用户: host key acceptance 或
// ssh-add -c key use confirmation.
func isConfirmPrompt(lower string) bool {
	for _, m := range []string{
		"are you sure you want to continue connecting",
		"yes/no",
		"allow use of key",
	} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// keyInfoFromPrompt extracts short identifying hint 从 askpass
// 提示词: account 为 "用户@host's password:" prompts, key 路径
// 为 "Enter passphrase 为 key '...':" prompts, 和 key 为
// ssh-add's "Allow use 的 key ...?" questions.
func keyInfoFromPrompt(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, prefix := range []string{"passphrase for key", "passphrase for", "pin for key", "pin for", "allow use of key"} {
		if i := strings.Index(lower, prefix); i >= 0 {
			return tidy(keyPath(prompt[i+len(prefix):]))
		}
	}
	if i := strings.Index(lower, "'s password"); i >= 0 {
		return tidy(prompt[:i])
	}
	if i := strings.Index(lower, "password for "); i >= 0 {
		return tidy(prompt[i+len("password for "):])
	}
	return ""
}

// keyPath cleans remainder 的 提示词 那 names key: quoted
// paths keep their content, everything else loses surrounding
// punctuation.
func keyPath(rest string) string {
	rest = strings.TrimSpace(rest)
	if _, path, ok := strings.Cut(rest, "'"); ok {
		if key, _, ok := strings.Cut(path, "'"); ok && key != "" {
			return key
		}
	}
	if end := strings.IndexAny(rest, "?\n"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(strings.TrimRight(rest, "':"))
}

package styles

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
)

// ThemeFile represents standalone theme definition 存储 作为 JSON.
// 它 wraps Palette 用 optional base theme reference. Missing
// palette fields inherit 从 base.
type ThemeFile struct {
	Base string `json:"base,omitempty"`
	Palette
}

// knownThemeFields 是 set 的 valid JSON 字段 names 在 theme 文件.
// Derived 从 PaletteFields 在 init time 所以 it stays 在 sync 用
// Palette struct automatically.
var knownThemeFields map[string]bool

func init() {
	knownThemeFields = make(map[string]bool, len(PaletteFields())+1)
	knownThemeFields["base"] = true
	for _, f := range PaletteFields() {
		knownThemeFields[f.Name] = true
	}
}

// LoadThemeFile reads 和 validates theme 文件 从 disk. Unknown
// fields 是 logged 作为 warnings 但 otherwise ignored. Returns 错误
// 如果 文件 cannot 是 读取 或 contains invalid color values.
func LoadThemeFile(path string) (*ThemeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read theme file: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse theme file: %w", err)
	}

	for key := range raw {
		if !knownThemeFields[key] {
			slog.Warn("Unknown field in theme file", "field", key, "path", path)
		}
	}

	var tf ThemeFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("decode theme file: %w", err)
	}

	if err := tf.Validate(); err != nil {
		return nil, fmt.Errorf("theme file %s: %w", filepath.Base(path), err)
	}

	return &tf, nil
}

// SaveThemeFile writes theme 文件 到 disk 用 indented JSON. Parent
// directories 是 created automatically.
func SaveThemeFile(path string, tf *ThemeFile) error {
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("encode theme file: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create theme directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write theme file: %w", err)
	}
	return nil
}

// themeDirsOverride allows tests 到 replace 默认 theme directories.
var themeDirsOverride []string

// ThemeDirs returns 用户 theme 目录. Theme files 是 global 所以
// launching Crush 从 different working directories always resolves same
// themes. Themes live 在 Crush 配置 目录 ( one holding
// global 配置 文件) under themes/, which honors CRUSH_GLOBAL_CONFIG 和
// matches README 在 every platform.
func ThemeDirs() []string {
	if themeDirsOverride != nil {
		return themeDirsOverride
	}
	return []string{filepath.Join(filepath.Dir(config.GlobalConfig()), "themes")}
}

// ThemePath returns 路径 为 validated global 用户 theme name.
func ThemePath(name string) (string, error) {
	name = strings.ToLower(name)
	if err := validateThemeNameFormat(name); err != nil {
		return "", err
	}
	filename, err := findThemeFilename(name)
	if err != nil {
		return "", err
	}
	if filename != "" {
		return filepath.Join(ThemeDirs()[0], filename), nil
	}
	return filepath.Join(ThemeDirs()[0], name+".json"), nil
}

func findThemeFilename(name string) (string, error) {
	dir := ThemeDirs()[0]
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("list themes in %s: %w", dir, err)
	}
	target := strings.ToLower(name) + ".json"
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), target) {
			return entry.Name(), nil
		}
	}
	return "", nil
}

// FindThemeFile locates global theme 文件 通过 name. Returns 错误 为
// invalid name 或 当 no matching 文件 exists.
func FindThemeFile(name string) (string, error) {
	path, err := ThemePath(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	filename, err := findThemeFilename(name)
	if err != nil {
		return "", err
	}
	if filename != "" {
		return filepath.Join(ThemeDirs()[0], filename), nil
	}
	return "", fmt.Errorf("theme file %q not found", strings.ToLower(name))
}

// validThemeName matches safe theme names: lowercase alphanumerics
// separated 通过 single hyphens 或 underscores.
var validThemeName = regexp.MustCompile(`^[a-z0-9]+([-_][a-z0-9]+)*$`)

// ThemeNameFormatHint 是 longest 消息 theme-name validators can
// return. Dialogs reserve space 为 it 所以 they do 不 grow 当 错误
// appears.
const ThemeNameFormatHint = "use lowercase letters, numbers, hyphens, and underscores only"

func validateThemeNameFormat(name string) error {
	if name == "" {
		return errors.New("theme name cannot be empty")
	}
	if !validThemeName.MatchString(name) {
		return errors.New(ThemeNameFormatHint)
	}
	return nil
}

// ValidateThemeName reports whether name 是 usable 作为 theme 文件 name.
// 它 rejects empty names, names 用 characters 那 是 unsafe 在 文件
// paths, 和 names 那 collide 用 existing theme.
func ValidateThemeName(name string) error {
	if err := validateThemeNameFormat(name); err != nil {
		return err
	}
	if IsBuiltinTheme(name) {
		return fmt.Errorf("%q is a built-in theme; edit it instead", name)
	}
	if _, err := FindThemeFile(name); err == nil {
		return fmt.Errorf("theme %q already exists", name)
	}
	return nil
}

// ValidateThemeRename 是 like ValidateThemeName 但 allows renaming
// theme 到 itself (case-insensitive). 它 still rejects builtins 和
// collisions 用 other themes.
func ValidateThemeRename(oldName, newName string) error {
	if err := validateThemeNameFormat(newName); err != nil {
		return err
	}
	lower := strings.ToLower(newName)
	if IsBuiltinTheme(lower) {
		return fmt.Errorf("%q is a built-in theme", lower)
	}
	if lower != strings.ToLower(oldName) {
		if _, err := FindThemeFile(lower); err == nil {
			return fmt.Errorf("theme %q already exists", lower)
		}
	}
	return nil
}

// RenameThemeFile renames 用户 theme 文件 在 disk. 它 locates
// existing 文件 via FindThemeFile, validates new name, 和 moves
// 文件 在 same 目录. Returns 错误 如果 source 文件
// 是 不 found, new name 是 invalid, 或 theme 用 new name
// already exists.
func RenameThemeFile(oldName, newName string) (string, string, error) {
	if err := ValidateThemeRename(oldName, newName); err != nil {
		return "", "", err
	}
	oldPath, err := FindThemeFile(oldName)
	if err != nil {
		return "", "", fmt.Errorf("rename theme: %w", err)
	}
	newPath := filepath.Join(filepath.Dir(oldPath), strings.ToLower(newName)+".json")
	if err := os.Rename(oldPath, newPath); err != nil {
		return "", "", fmt.Errorf("rename theme file: %w", err)
	}
	return oldPath, newPath, nil
}

// DeleteThemeFile removes 用户 theme 文件 从 disk. 它 rejects
// built-在 themes 和 returns 错误 当 theme 是 不 found.
func DeleteThemeFile(name string) error {
	lower := strings.ToLower(name)
	if IsBuiltinTheme(lower) {
		return fmt.Errorf("%q is a built-in theme", lower)
	}
	path, err := FindThemeFile(lower)
	if err != nil {
		return fmt.Errorf("delete theme: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete theme file: %w", err)
	}
	return nil
}

// ListUserThemes returns names 的 all 用户-defined themes found
// across all theme directories. Names 是 lowercased 和 deduplicated;
// earlier directories take priority.
func ListUserThemes() ([]string, error) {
	seen := make(map[string]bool)
	var names []string

	for _, dir := range ThemeDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("list themes in %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			lower := strings.ToLower(name)
			if validateThemeNameFormat(lower) != nil {
				slog.Warn("Ignoring invalid theme filename", "file", e.Name(), "directory", dir)
				continue
			}
			if !seen[lower] {
				seen[lower] = true
				names = append(names, lower)
			}
		}
	}
	return names, nil
}

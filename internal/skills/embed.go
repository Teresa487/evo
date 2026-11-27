package skills

import (
	"embed"
	"io/fs"
	"log/slog"
	"path/filepath"
)

// BuiltinPrefix 是 路径 prefix 为 builtin skill files. 它 是 used 通过
// View 工具 到 distinguish embedded files 从 disk files.
const BuiltinPrefix = "crush://skills/"

//go:embed builtin/*
var builtinFS embed.FS

// BuiltinFS returns the embedded filesystem containing builtin skills.
func BuiltinFS() embed.FS {
	return builtinFS
}

// DiscoverBuiltin finds all valid skills embedded 在 二进制.
func DiscoverBuiltin() []*Skill {
	skills, _ := DiscoverBuiltinWithStates()
	return skills
}

// DiscoverBuiltinWithStates 是 like DiscoverBuiltin 但 additionally returns
// per-文件 state slice describing parse/validation outcomes. Useful 为
// diagnostics.
func DiscoverBuiltinWithStates() ([]*Skill, []*SkillState) {
	var discovered []*Skill
	var states []*SkillState

	fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || d.Name() != SkillFileName {
			return nil
		}

		content, err := builtinFS.ReadFile(path)
		if err != nil {
			slog.Warn("Failed to read builtin skill file", "path", path, "error", err)
			states = append(states, &SkillState{Path: path, State: StateError, Err: err})
			return nil
		}

		skill, err := ParseContent(content)
		if err != nil {
			slog.Warn("Failed to parse builtin skill file", "path", path, "error", err)
			states = append(states, &SkillState{Path: path, State: StateError, Err: err})
			return nil
		}

		// Set paths using the crush prefix. Strip the leading "builtin/"
		// 所以 路径 是 relative 到 embedded root
		// (e.g., "crush://skills/crush-配置/SKILL.md").
		relPath, _ := filepath.Rel("builtin", path)
		relPath = filepath.ToSlash(relPath)
		skill.SkillFilePath = BuiltinPrefix + relPath
		skill.Path = BuiltinPrefix + filepath.Dir(relPath)
		skill.Builtin = true

		if err := skill.Validate(); err != nil {
			slog.Warn("Builtin skill validation failed", "path", path, "error", err)
			states = append(states, &SkillState{Name: skill.Name, Path: path, State: StateError, Err: err})
			return nil
		}

		slog.Debug("Successfully loaded builtin skill", "name", skill.Name, "path", skill.SkillFilePath)
		discovered = append(discovered, skill)
		states = append(states, &SkillState{Name: skill.Name, Path: skill.SkillFilePath, State: StateNormal})
		return nil
	})

	return discovered, states
}

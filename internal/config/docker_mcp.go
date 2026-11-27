// docker_mcp.go —— Docker 型 MCP 服务器的配置解析。

package config

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

var dockerMCPVersionRunner = func(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "mcp", "version")
	return cmd.Run()
}

const dockerMCPAvailabilityTTL = 10 * time.Second

var dockerMCPAvailabilityCache struct {
	mu        sync.Mutex
	available bool
	checkedAt time.Time
	known     bool
}

// DockerMCPName 是 name 的 Docker MCP configuration.
const DockerMCPName = "docker"

// IsDockerMCPAvailable checks 如果 Docker MCP 是 available 通过 running
// 'docker mcp version'.
func IsDockerMCPAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := dockerMCPVersionRunner(ctx)
	return err == nil
}

// DockerMCPAvailabilityCached returns cached Docker MCP availability 和
// whether cached 值 是 still fresh.
func DockerMCPAvailabilityCached() (available bool, known bool) {
	dockerMCPAvailabilityCache.mu.Lock()
	defer dockerMCPAvailabilityCache.mu.Unlock()

	if !dockerMCPAvailabilityCache.known {
		return false, false
	}
	if time.Since(dockerMCPAvailabilityCache.checkedAt) > dockerMCPAvailabilityTTL {
		return dockerMCPAvailabilityCache.available, false
	}
	return dockerMCPAvailabilityCache.available, true
}

// RefreshDockerMCPAvailability refreshes 和 caches Docker MCP availability.
func RefreshDockerMCPAvailability() bool {
	available := IsDockerMCPAvailable()
	dockerMCPAvailabilityCache.mu.Lock()
	dockerMCPAvailabilityCache.available = available
	dockerMCPAvailabilityCache.checkedAt = time.Now()
	dockerMCPAvailabilityCache.known = true
	dockerMCPAvailabilityCache.mu.Unlock()
	return available
}

// IsDockerMCPEnabled checks 如果 Docker MCP 是 already configured.
func (c *Config) IsDockerMCPEnabled() bool {
	if c.MCP == nil {
		return false
	}
	_, exists := c.MCP[DockerMCPName]
	return exists
}

// DockerMCPConfig returns 默认 Docker MCP stdio configuration.
func DockerMCPConfig() MCPConfig {
	return MCPConfig{
		Type:     MCPStdio,
		Command:  "docker",
		Args:     []string{"mcp", "gateway", "run"},
		Disabled: false,
	}
}

// PrepareDockerMCPConfig validates Docker MCP availability 和 stages
// Docker MCP configuration 在 memory.
func (s *ConfigStore) PrepareDockerMCPConfig() (MCPConfig, error) {
	if !IsDockerMCPAvailable() {
		return MCPConfig{}, fmt.Errorf("docker mcp is not available, please ensure docker is installed and 'docker mcp version' succeeds")
	}

	mcpConfig := DockerMCPConfig()
	// In-memory 只; persistence happens 在 PersistDockerMCPConfig.
	s.mutateInMemory(func(c *Config) {
		if c.MCP == nil {
			c.MCP = make(map[string]MCPConfig)
		}
		c.MCP[DockerMCPName] = mcpConfig
	})
	return mcpConfig, nil
}

// PersistDockerMCPConfig persists a previously prepared Docker MCP
// configuration 到 global 配置 文件.
func (s *ConfigStore) PersistDockerMCPConfig(mcpConfig MCPConfig) error {
	if err := s.SetConfigField(ScopeGlobal, "mcp."+DockerMCPName, mcpConfig); err != nil {
		return fmt.Errorf("failed to persist docker mcp configuration: %w", err)
	}
	return nil
}

// EnableDockerMCP adds Docker MCP configuration 和 persists it.
func (s *ConfigStore) EnableDockerMCP() error {
	mcpConfig, err := s.PrepareDockerMCPConfig()
	if err != nil {
		return err
	}
	if err := s.PersistDockerMCPConfig(mcpConfig); err != nil {
		return err
	}
	return nil
}

// DisableDockerMCP removes Docker MCP configuration 和 persists change.
func (s *ConfigStore) DisableDockerMCP() error {
	return s.update(ScopeGlobal, func(c *Config) map[string]any {
		if c.MCP == nil {
			return nil
		}
		delete(c.MCP, DockerMCPName)
		return map[string]any{"mcp": c.MCP}
	})
}

// RemoveDockerMCPInMemory removes Docker MCP 条目 从 在-memory
// 配置 via copy-在-写入, without persisting 到 disk. 它 rolls back
// staged PrepareDockerMCPConfig 当 starting 或 persisting 服务端
// fails, 所以 callers must 不 mutate 配置().MCP directly.
func (s *ConfigStore) RemoveDockerMCPInMemory() {
	s.mutateInMemory(func(c *Config) {
		delete(c.MCP, DockerMCPName)
	})
}

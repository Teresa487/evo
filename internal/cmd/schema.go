// schema.go —— 生成配置 JSON schema。

package cmd

import (
	"encoding/json"
	"fmt"

	"charm.land/catwalk/v3/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/discover"
	"github.com/invopop/jsonschema"
	"github.com/spf13/cobra"
)

var schemaCmd = &cobra.Command{
	Use:    "schema",
	Short:  "Generate JSON schema for configuration",
	Long:   "Generate JSON schema for the crush configuration file",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		reflector := new(jsonschema.Reflector)
		schema := reflector.Reflect(&config.Config{})
		setProviderTypeEnum(schema)
		bts, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal schema: %w", err)
		}
		fmt.Println(string(bts))
		return nil
	},
}

// setProviderTypeEnum overwrites 提供方 `type` enum 用 live set
// 的 accepted values rather than hand-maintained struct tag. values
// must match exactly what load.go validates against: catwalk 提供方
// types, Charm Hyper type, 和 any locally-discovered providers 那
// self-register enricher (e.g. ollama, omlx). Sourcing enum here 保持
// published schema 从 drifting 作为 提供方 types 是 added 或 renamed.
func setProviderTypeEnum(schema *jsonschema.Schema) {
	def, ok := schema.Definitions["ProviderConfig"]
	if !ok || def.Properties == nil {
		return
	}
	typeProp, ok := def.Properties.Get("type")
	if !ok {
		return
	}

	var types []string
	for _, t := range catwalk.KnownProviderTypes() {
		types = append(types, string(t))
	}
	types = append(types, string(hyper.Name))
	types = append(types, discover.RegisteredProviderTypes()...)

	typeProp.Enum = make([]any, len(types))
	for i, t := range types {
		typeProp.Enum[i] = t
	}
}

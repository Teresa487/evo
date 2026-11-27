package apigen

import (
	"reflect"
	"strings"
	"time"
)

// Schema 是 JSON-Schema-shaped description 的 Go 值, derived 通过
// reflection. 它 supports subset 的 JSON Schema used 通过 docs
// page 和 OpenAPI document.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
}

var (
	timeType  = reflect.TypeOf(time.Time{})
	errorType = reflect.TypeOf((*error)(nil)).Elem()
)

// SchemaOf derives [Schema] 从 type 的 v, honoring encoding/json
// tags: 字段 names come 从 json tags, 和 fields marked omitempty 是
// 不 required.
//
// Structs 从 same package 作为 v's type 是 expanded recursively;
// structs 从 other packages (和 true type cycles) 是 rendered 作为
// free-form objects described 通过 their type name, which keeps schemas
// 为 wide external types honest without inventing shape.
func SchemaOf(v any) *Schema {
	t := reflect.TypeOf(v)
	if t == nil {
		return &Schema{}
	}
	return schemaOf(t, rootPkg(t), map[reflect.Type]bool{})
}

// rootPkg returns package 路径 的 (possibly 指针) type v's
// named root, used 到 bound struct expansion 到 one package.
func rootPkg(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath()
}

func schemaOf(t reflect.Type, pkg string, expanding map[reflect.Type]bool) *Schema {
	switch t {
	case timeType:
		return &Schema{Type: "string", Format: "date-time"}
	case errorType:
		return &Schema{Type: "string"}
	}

	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem(), pkg, expanding)
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: schemaOf(t.Elem(), pkg, expanding)}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return &Schema{Type: "object"}
		}
		return &Schema{Type: "object", AdditionalProperties: schemaOf(t.Elem(), pkg, expanding)}
	case reflect.Struct:
		return structSchema(t, pkg, expanding)
	default:
		// Interfaces (including any) 和 anything else: free-form.
		return &Schema{}
	}
}

func structSchema(t reflect.Type, pkg string, expanding map[reflect.Type]bool) *Schema {
	if t.PkgPath() != pkg {
		return &Schema{Type: "object", Description: t.String()}
	}
	if expanding[t] {
		return &Schema{Type: "object", Description: t.String()}
	}
	expanding[t] = true
	defer delete(expanding, t)

	s := &Schema{Type: "object"}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			// Embedded structs contribute their fields, per
			// encoding/json semantics, even 当 embedded
			// type itself 是 unexported.
			emb := schemaOf(f.Type, pkg, expanding)
			for k, v := range emb.Properties {
				if s.Properties == nil {
					s.Properties = map[string]*Schema{}
				}
				s.Properties[k] = v
			}
			s.Required = append(s.Required, emb.Required...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if s.Properties == nil {
			s.Properties = map[string]*Schema{}
		}
		s.Properties[name] = schemaOf(f.Type, pkg, expanding)
		if !strings.Contains(opts, "omitempty") {
			s.Required = append(s.Required, name)
		}
	}
	return s
}

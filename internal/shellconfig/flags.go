package shellconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
)

// flagKind 是 值 type flag parses 从 command line.
type flagKind int

const (
	flagString flagKind = iota
	flagBool
	// flagBoolTrue 是 valueless boolean flag (e.g. --think) 那 stores
	// true 当 present, without consuming argument.
	flagBoolTrue
	flagInt
	flagFloat
	// flagKeyValue consumes two args (KEY 值) 和 stores them 作为 map
	// 条目, e.g. --env NAME 值.
	flagKeyValue
	// flagJSONObject parses 值 作为 JSON object (map), rejecting
	// arrays 和 scalars.
	flagJSONObject
	// flagJSONAny parses 值 作为 arbitrary JSON.
	flagJSONAny
)

// flagOp 是 how parsed flag 值 是 written into target map.
type flagOp int

const (
	// opSet assigns target[jsonKey] = 值.
	opSet flagOp = iota
	// opAppend appends 值 到 []any 在 target[jsonKey].
	opAppend
	// opSetChild assigns childMap(target, child)[jsonKey] = 值, e.g.
	// single --env KEY 值 条目 under "env" object.
	opSetChild
	// opMergeChild merges a JSON object into childMap(target, child), e.g.
	// --提供方-options '{...}'.
	opMergeChild
	// opSetInChild assigns childMap(target, child)[jsonKey] = 值, e.g.
	// --price-input F 存储 under pricing.input.
	opSetInChild
)

// flagSpec declares one command-line flag: how it parses, where it writes,
// 和 optional validator. 一个 builtin's whole flag surface 是 []flagSpec
// handed 到 applyFlags, which replaces per-builtin parse loops.
type flagSpec struct {
	name    string // long flag including dashes, e.g. "--api-key"
	jsonKey string // destination key in the target map
	kind    flagKind
	op      flagOp
	child   string // child map name for opSetChild / opMergeChild

	// template marks fields 配置 loader shell-expands 在 use time,
	// 作为 在 crush.json. Their values 是 escaped 到 expand 到 themselves.
	template bool

	// validate, 如果 non-nil, checks parsed 值 before it 是 存储.
	// 它 receives 值 作为 string, bool, int64, float64, 或
	// map[string]any depending 在 kind.
	validate func(any) error
}

// applyFlags parses args[start:] against specs 和 writes results into
// target. cmd names invoking command 为 错误 messages (e.g.
// "提供方 add"). An unrecognized flag 是 错误.
func applyFlags(specs []flagSpec, args []string, start int, target map[string]any, cmd string, stderr io.Writer) error {
	i := start
	for i < len(args) {
		spec, ok := findFlag(specs, args[i])
		if !ok {
			return usage(stderr, fmt.Sprintf("%s: unknown flag %s", cmd, args[i]))
		}

		val, next, err := parseFlagValue(spec, args, i)
		if err != nil {
			return usage(stderr, err.Error())
		}
		if spec.validate != nil {
			if err := spec.validate(val); err != nil {
				return usage(stderr, fmt.Sprintf("%s: %s", cmd, err))
			}
		}
		if spec.template {
			val = escapeTemplateValue(val)
		}
		storeFlag(target, spec, val)
		i = next
	}
	return nil
}

// templateEscaper escapes characters special 在 here-document word,
// form 配置 loader uses 到 expand template fields.
var templateEscaper = strings.NewReplacer(`\`, `\\`, `$`, `\$`, "`", "\\`")

// escapeTemplateValue escapes parsed string 或 KEY 值 flag 值.
func escapeTemplateValue(val any) any {
	switch v := val.(type) {
	case string:
		return templateEscaper.Replace(v)
	case [2]string:
		return [2]string{v[0], templateEscaper.Replace(v[1])}
	}
	return val
}

func findFlag(specs []flagSpec, name string) (flagSpec, bool) {
	for _, s := range specs {
		if s.name == name {
			return s, true
		}
	}
	return flagSpec{}, false
}

// parseFlagValue reads 值(s) 为 spec starting 在 args[i] 和 returns
// parsed 值 plus index 到 resume 从.
func parseFlagValue(spec flagSpec, args []string, i int) (any, int, error) {
	name := strings.TrimPrefix(spec.name, "--")

	switch spec.kind {
	case flagString:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		return v, i + 2, nil

	case flagBoolTrue:
		return true, i + 1, nil

	case flagBool:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		b, err := parseBool(v)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: --%s expects true/false, got %q", args[0], name, v)
		}
		return b, i + 2, nil

	case flagInt:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: --%s expects an integer, got %q", args[0], name, v)
		}
		return n, i + 2, nil

	case flagFloat:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: --%s expects a number, got %q", args[0], name, v)
		}
		return f, i + 2, nil

	case flagKeyValue:
		if i+2 >= len(args) {
			return nil, 0, fmt.Errorf("%s: --%s requires a key and value", args[0], name)
		}
		return [2]string{args[i+1], args[i+2]}, i + 3, nil

	case flagJSONObject:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(v), &object); err != nil || object == nil {
			return nil, 0, fmt.Errorf("%s: --%s expects a JSON object, got %q", args[0], name, v)
		}
		return object, i + 2, nil

	case flagJSONAny:
		v, err := nextArg(args, i, name)
		if err != nil {
			return nil, 0, err
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, 0, fmt.Errorf("%s: --%s expects valid JSON, got %q: %s", args[0], name, v, err)
		}
		return parsed, i + 2, nil

	default:
		return nil, 0, fmt.Errorf("%s: --%s has unknown flag kind", args[0], name)
	}
}

// nextArg returns args[i+1], erroring 如果 flag 是 missing 它的 值.
func nextArg(args []string, i int, flag string) (string, error) {
	if i+1 >= len(args) {
		return "", fmt.Errorf("%s: --%s requires a value", args[0], flag)
	}
	return args[i+1], nil
}

// storeFlag writes parsed 值 into target according 到 spec.op.
func storeFlag(target map[string]any, spec flagSpec, val any) {
	switch spec.op {
	case opSet:
		target[spec.jsonKey] = val
	case opAppend:
		arr, _ := target[spec.jsonKey].([]any)
		target[spec.jsonKey] = append(arr, val)
	case opSetChild:
		if kv, ok := val.([2]string); ok {
			childMap(target, spec.child)[kv[0]] = kv[1]
		}
	case opMergeChild:
		if obj, ok := val.(map[string]any); ok {
			maps.Copy(childMap(target, spec.child), obj)
		}
	case opSetInChild:
		childMap(target, spec.child)[spec.jsonKey] = val
	}
}

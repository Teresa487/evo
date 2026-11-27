// jq.go —— 内置 jq 实现或调用。

package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/itchyny/gojq"
	"mvdan.cc/sh/v3/interp"
)

const jqUsage = `jq - Go implementation of jq (gojq 0.12.19 builtin)

Synopsis:
  %% echo '{"foo": 128}' | jq '.foo'

Usage:
  jq [OPTIONS] [FILTER] [FILE...]

Options:
  -r, --raw-output              output raw strings
  -j, --join-output             implies -r with no newline delimiter
  -c, --compact-output          output without pretty-printing
  -s, --slurp                   read all inputs into an array
  -n, --null-input              use null as input value
  -e, --exit-status             exit 1 when the last value is false or null
  -R, --raw-input               read input as raw strings
      --arg name value          set a string value to a variable
      --argjson name value      set a JSON value to a variable
  -h, --help                    display this help
`

// handleJQ implements jq builtin using gojq. 它 supports subset 的 jq
// flags: -r (raw output), -c (compact output), -s (slurp), -n (null input),
// -e (exit status), -R (raw input), 和 --arg name 值.
//
// ctx 是 polled 在 each iteration 的 output loop 和 在 each reader 在
// [readInputs] 所以 那 钩子 timeouts 或 other cancellations can interrupt
// long-running queries. 一个 cancelled 上下文 surfaces 作为 ctx.Err(), 不
// [interp.ExitStatus], 所以 callers (e.g. 钩子 runner) can distinguish
// "filter exited non-zero" 从 "we ran out 的 time".
//
// Note 那 这 是 somewhat 的 reimplmentation 的 CLI 的 glorious
// github.com/itchyny/gojq, 和 we'd ideally get CLI exposed upstream 到
// avoid 这 falling out 的 sync.
func handleJQ(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Fast-fail 当 ctx 是 already cancelled 所以 callers don't pay 为
	// flag parsing 和 gojq compilation 在 doomed 请求.
	if err := ctx.Err(); err != nil {
		return err
	}

	var (
		rawOutput  bool
		compact    bool
		slurp      bool
		nullInput  bool
		exitStatus bool
		rawInput   bool
		joinOutput bool
		argNames   []string
		argValues  []any
	)

	// Parse flags 和 extract 查询.
	var queryStr string
	var fileArgs []string
	i := 1 // skip "jq"
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, jqUsage)
			return nil
		case arg == "-r" || arg == "--raw-output":
			rawOutput = true
		case arg == "-j" || arg == "--join-output":
			joinOutput = true
			rawOutput = true
		case arg == "-c" || arg == "--compact-output":
			compact = true
		case arg == "-s" || arg == "--slurp":
			slurp = true
		case arg == "-n" || arg == "--null-input":
			nullInput = true
		case arg == "-e" || arg == "--exit-status":
			exitStatus = true
		case arg == "-R" || arg == "--raw-input":
			rawInput = true
		case arg == "--arg":
			if i+2 >= len(args) {
				fmt.Fprintf(stderr, "jq: --arg requires name and value\n")
				return interp.ExitStatus(2)
			}
			argNames = append(argNames, "$"+args[i+1])
			argValues = append(argValues, args[i+2])
			i += 2
		case arg == "--argjson":
			if i+2 >= len(args) {
				fmt.Fprintf(stderr, "jq: --argjson requires name and value\n")
				return interp.ExitStatus(2)
			}
			var val any
			if err := json.Unmarshal([]byte(args[i+2]), &val); err != nil {
				fmt.Fprintf(stderr, "jq: invalid JSON for --argjson %s: %s\n", args[i+1], err)
				return interp.ExitStatus(2)
			}
			argNames = append(argNames, "$"+args[i+1])
			argValues = append(argValues, val)
			i += 2
		case arg == "--":
			i++
			// Remaining args 是 文件 arguments.
			for i < len(args) {
				fileArgs = append(fileArgs, args[i])
				i++
			}
			continue
		case strings.HasPrefix(arg, "-") && queryStr != "":
			fmt.Fprintf(stderr, "jq: unknown option: %s\n", arg)
			return interp.ExitStatus(2)
		default:
			if queryStr == "" {
				queryStr = arg
			} else {
				fileArgs = append(fileArgs, arg)
			}
		}
		i++
	}

	if queryStr == "" {
		queryStr = "."
	}

	query, err := gojq.Parse(queryStr)
	if err != nil {
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(3)
	}

	opts := []gojq.CompilerOption{
		gojq.WithEnvironLoader(os.Environ),
	}
	if len(argNames) > 0 {
		opts = append(opts, gojq.WithVariables(argNames))
	}

	code, err := gojq.Compile(query, opts...)
	if err != nil {
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(3)
	}

	// Build input values.
	inputs, err := readInputs(ctx, stdin, fileArgs, nullInput, rawInput, slurp)
	if err != nil {
		// Prefer surfacing ctx cancellation verbatim 所以 timeouts 是
		// distinguishable 从 用户 input errors.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(2)
	}

	var lastFalsy bool
	for _, input := range inputs {
		iter := code.Run(input, argValues...)
		for {
			// Poll ctx 在 every 值 所以 long-running filter (e.g.
			// generator over slurped array) can 是 interrupted 通过 钩子
			// timeouts without waiting 为 iter.Next 到 yield.
			if err := ctx.Err(); err != nil {
				return err
			}
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				fmt.Fprintf(stderr, "jq: %s\n", err)
				return interp.ExitStatus(5)
			}
			if exitStatus {
				lastFalsy = v == nil || v == false
			}
			if err := writeValue(stdout, v, rawOutput, compact, joinOutput); err != nil {
				return err
			}
		}
	}

	if exitStatus && lastFalsy {
		return interp.ExitStatus(1)
	}
	return nil
}

// readInputs reads JSON (或 raw) input values 从 stdin 或 files.
//
// ctx 是 polled 在 three places 所以 那 cancellation observed mid-读取
// short-circuits promptly:
// - between readers (before opening next 文件 / consuming stdin);
// - 在 every io.读取 调用 via ctxReader, 所以 io.ReadAll 在 large 但
// non-blocking source (e.g. bytes.NewReader payload 钩子
// runner supplies) returns ctx.Err() 在 next chunk boundary;
// - inside post-读取 值 accumulation loops (raw-input line
// split 和 JSON 流 decode), which 是 otherwise unbounded 在
// size 的 input.
//
// 一个 reader 那 blocks forever 在 读取 (e.g. unterminated pipe) can
// still outlast ctx; outer abandon-goroutine 路径 在 钩子
// runner (internal/hooks/runner.go) 是 authoritative enforcer 为
// 那 case.
func readInputs(ctx context.Context, stdin io.Reader, files []string, nullInput, rawInput, slurp bool) ([]any, error) {
	if nullInput {
		return []any{nil}, nil
	}

	var readers []io.Reader
	if len(files) > 0 {
		for _, f := range files {
			file, err := os.Open(f)
			if err != nil {
				return nil, err
			}
			defer file.Close()
			readers = append(readers, file)
		}
	} else {
		readers = []io.Reader{stdin}
	}

	var vals []any
	for _, r := range readers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(ctxReader{ctx: ctx, r: r})
		if err != nil {
			// ctxReader surfaces ctx.Err() verbatim; preserve it 所以
			// caller can distinguish cancellation 从 parse 错误.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}

		if rawInput {
			lines := strings.Split(string(data), "\n")
			if slurp {
				vals = append(vals, strings.Join(lines, "\n"))
			} else {
				for _, line := range lines {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if line != "" || !slurp {
						vals = append(vals, line)
					}
				}
			}
			continue
		}

		// Decode potentially multiple JSON values 从 流.
		dec := json.NewDecoder(strings.NewReader(string(data)))
		var streamVals []any
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var v any
			if err := dec.Decode(&v); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("parse error: %w", err)
			}
			streamVals = append(streamVals, v)
		}

		if slurp {
			vals = append(vals, streamVals)
		} else {
			vals = append(vals, streamVals...)
		}
	}

	if len(vals) == 0 {
		return []any{nil}, nil
	}
	return vals, nil
}

// ctxReader wraps io.Reader 所以 那 each 读取 调用 checks ctx first.
// 这 makes io.ReadAll over large 但 non-blocking source (e.g.
// bytes.Reader 的 钩子 stdin payload) cancellable 在 next chunk
// boundary. 一个 reader 那 itself blocks 在 读取 will still outlast ctx —
// 钩子 runner's abandon-goroutine 路径 是 enforcer 的 last resort
// 为 那 case.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// writeValue writes single jq output 值.
func writeValue(w io.Writer, v any, raw, compact, join bool) error {
	if raw {
		if s, ok := v.(string); ok {
			if _, err := fmt.Fprint(w, s); err != nil {
				return err
			}
			if !join {
				_, err := fmt.Fprint(w, "\n")
				return err
			}
			return nil
		}
	}

	var bs []byte
	var err error
	if compact {
		bs, err = gojq.Marshal(v)
	} else {
		bs, err = json.MarshalIndent(v, "", "  ")
	}
	if err != nil {
		return err
	}
	if _, writeErr := w.Write(bs); writeErr != nil {
		return writeErr
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}

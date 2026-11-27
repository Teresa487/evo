package shell

// Example usage 的 shell package:
//
// 1. For one-off commands:
//
//	shell := shell.NewShell(nil)
// stdout, stderr, err := shell.Exec(上下文.Background(), "echo hello")
//
// 2. For maintaining state across commands:
//
//	shell := shell.NewShell(&shell.Options{
//	    WorkingDir: "/tmp",
//	    Logger: myLogger,
//	})
//	shell.Exec(ctx, "export FOO=bar")
// shell.Exec(ctx, "echo $FOO") // 将 print "bar"
//
// 3. Managing environment 和 working 目录:
//
//	shell := shell.NewShell(nil)
// shell.SetEnv("MY_VAR", "值")
//	shell.SetWorkingDir("/tmp")
//	cwd := shell.GetWorkingDir()
//	env := shell.GetEnv()

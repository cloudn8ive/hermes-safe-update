package execx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is re-executed as a child by the tests below.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("EXECX_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch args[0] {
	case "echo":
		fmt.Println(strings.Join(args[1:], " "))
		fmt.Fprintln(os.Stderr, "to-stderr")
		os.Exit(0)
	case "exit":
		var code int
		fmt.Sscan(args[1], &code)
		os.Exit(code)
	case "env":
		fmt.Printf("HERMES_HOME=%q EXTRA=%q\n", os.Getenv("HERMES_HOME"), os.Getenv("EXTRA"))
		os.Exit(0)
	case "lines":
		for _, l := range args[1:] {
			fmt.Println(l)
		}
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(99)
}

func helper(args ...string) Cmd {
	return Cmd{
		Argv: append([]string{os.Args[0], "-test.run=TestHelperProcess", "--"}, args...),
		Env:  map[string]string{"EXECX_HELPER": "1"},
	}
}

func TestRunCapturesOutputAndCode(t *testing.T) {
	r := New()
	res, err := r.Run(context.Background(), helper("echo", "hello", "world"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 || !strings.Contains(res.Output, "hello world") || !strings.Contains(res.Output, "to-stderr") {
		t.Errorf("res = %+v", res)
	}
	if strings.Index(res.Output, "hello") > strings.Index(res.Output, "to-stderr") {
		t.Errorf("stdout must come before stderr: %q", res.Output)
	}
	res, err = r.Run(context.Background(), helper("exit", "3"))
	if err != nil || res.Code != 3 {
		t.Errorf("exit code: %+v %v", res, err)
	}
}

func TestRunStripsHermesHomeAndAddsEnv(t *testing.T) {
	t.Setenv("HERMES_HOME", "/somewhere")
	c := helper("env")
	c.Env["EXTRA"] = "x y"
	res, err := New().Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `HERMES_HOME=""`) || !strings.Contains(res.Output, `EXTRA="x y"`) {
		t.Errorf("env not as expected: %q", res.Output)
	}
}

func TestRunTimeoutIs124(t *testing.T) {
	c := helper("sleep")
	c.Timeout = 300 * time.Millisecond
	start := time.Now()
	res, err := New().Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != CodeTimeout || !res.TimedOut {
		t.Errorf("res = %+v", res)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("timeout took too long: %v", time.Since(start))
	}
}

func TestStreamTimeoutCallsKillTreeWithChildPid(t *testing.T) {
	c := helper("sleep")
	c.Timeout = 300 * time.Millisecond
	var got []int
	c.KillTree = func(pid int) error {
		got = append(got, pid)
		return errors.New("tree kill unavailable") // falls back to killing the child
	}
	start := time.Now()
	res, err := New().Stream(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Code != CodeTimeout {
		t.Errorf("res = %+v", res)
	}
	if len(got) != 1 || got[0] <= 0 {
		t.Errorf("KillTree calls = %v, want one call with the child's pid", got)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("fallback kill took too long: %v", time.Since(start))
	}
}

func TestKillTreeNotCalledWhenChildExitsInTime(t *testing.T) {
	c := helper("exit", "0")
	called := false
	c.KillTree = func(int) error { called = true; return nil }
	if _, err := New().Stream(context.Background(), c, nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("KillTree called for a child that exited on its own")
	}
}

func TestRunMissingExecutableIs127(t *testing.T) {
	res, err := New().Run(context.Background(), Cmd{Argv: []string{"definitely-not-a-real-binary-xyz"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != CodeNotFound {
		t.Errorf("res = %+v", res)
	}
}

func TestRunEmptyArgvIsError(t *testing.T) {
	if _, err := New().Run(context.Background(), Cmd{}); !errors.Is(err, ErrEmptyArgv) {
		t.Errorf("err = %v", err)
	}
}

func TestStreamDeliversLines(t *testing.T) {
	var got []string
	res, err := New().Stream(context.Background(), helper("lines", "a", "b", "c"), func(l string) { got = append(got, l) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 || strings.Join(got, ",") != "a,b,c" {
		t.Errorf("got %v res %+v", got, res)
	}
}

func TestTailJoinsNonBlankLinesAndCuts(t *testing.T) {
	if got := Tail("a\n\n  b  \r\nc\n", 100); got != "a | b | c" {
		t.Errorf("got %q", got)
	}
	if got := Tail("abcdef", 3); got != "def" {
		t.Errorf("got %q", got)
	}
}

func TestFakeRunnerMatchesAndRecords(t *testing.T) {
	f := &Fake{}
	f.On([]string{"git", "rev-parse", "HEAD"}, Result{Output: "abc\n"})
	f.OnPrefix([]string{"hermes", "update"}, Result{Code: 1, Output: "x"})
	res, _ := f.Run(context.Background(), Cmd{Argv: []string{"git", "rev-parse", "HEAD"}})
	if res.Output != "abc\n" {
		t.Errorf("res = %+v", res)
	}
	var lines []string
	res, _ = f.Stream(context.Background(), Cmd{Argv: []string{"hermes", "update", "--yes"}}, func(l string) { lines = append(lines, l) })
	if res.Code != 1 || len(lines) != 1 {
		t.Errorf("stream res = %+v lines %v", res, lines)
	}
	res, _ = f.Run(context.Background(), Cmd{Argv: []string{"unknown"}})
	if res.Code != CodeNotFound {
		t.Errorf("unmatched command should be 127, got %+v", res)
	}
	if len(f.Calls()) != 3 {
		t.Errorf("calls = %v", f.Calls())
	}
}

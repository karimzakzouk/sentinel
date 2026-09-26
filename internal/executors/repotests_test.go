package executors

import (
	"testing"

	"github.com/karimzakzouk/sentinel/internal/repoci"
)

func TestClassifyBuildFailure(t *testing.T) {
	cases := map[string]string{
		// Run #27 search-api: Cargo.lock not copied -> edition2024 on rust 1.78.
		"The package requires the Cargo feature called `edition2024`, but that feature is not stabilized in this version of Cargo (1.78.0).\nerror building image: error building stage: failed to execute command: waiting for process to exit: exit status 101": "build",
		"[ERROR] Failed to execute goal ... Compilation failure\n[ERROR] /workspace/repo/src/Main.java:[10] cannot find symbol":                                                                                                                                   "build",
		"error building image: failed to authorize: 401 unauthorized":                              "infra",
		"error building image: pulling image: dial tcp: lookup registry-1.docker.io: no such host": "infra",
		"error building image: failed to push: denying repository access: TOOMANYREQUESTS":         "infra",
		"unlinkat //product_uuid: device or resource busy":                                         "infra",
		"": "infra",
	}
	for log, want := range cases {
		if got := ClassifyBuildFailure(log); got != want {
			t.Errorf("ClassifyBuildFailure(%q) = %q, want %q", log[:min(len(log), 60)], got, want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestParseRunOutput(t *testing.T) {
	ci := &repoci.Discovered{
		Language: "rust",
		FmtCmds:  []string{"cargo fmt --all"},
		TestCmds: []string{"cargo test"},
	}
	out := `cloned
===SENTINEL_CMD_START key=fmt-0 phase=fmt===
===SENTINEL_CMD_END key=fmt-0 exit=0===
===SENTINEL_READBACK_START===
===SENTINEL_FILE_START path=src/handlers.rs===
aGVsbG8gd29ybGQ=
===SENTINEL_FILE_END===
===SENTINEL_READBACK_END===
===SENTINEL_CMD_START key=test-0 phase=test===
test result: FAILED. 0 passed; 1 failed
test handlers::search ... FAILED
===SENTINEL_CMD_END key=test-0 exit=101===
===SENTINEL_DONE===
`
	res := parseRunOutput(out, ci)
	if len(res.Commands) != 2 {
		t.Fatalf("commands = %+v", res.Commands)
	}
	if res.Commands[0].Phase != "fmt" || res.Commands[0].Exit != 0 {
		t.Errorf("fmt cmd: %+v", res.Commands[0])
	}
	if res.Commands[1].Phase != "test" || res.Commands[1].Exit != 101 {
		t.Errorf("test cmd: %+v", res.Commands[1])
	}
	if !res.TestsRan {
		t.Error("TestsRan should be true")
	}
	if v, ok := res.Readback["src/handlers.rs"]; !ok || v != "hello world" {
		t.Errorf("readback = %q", v)
	}
	if len(res.FailedTests) != 1 || res.FailedTests[0] != "handlers::search" {
		t.Errorf("failed tests = %v", res.FailedTests)
	}
}

func TestCompareRepoTests(t *testing.T) {
	mk := func(failing bool, testNames ...string) *RunResult {
		r := &RunResult{Readback: map[string]string{}}
		out := ""
		for _, n := range testNames {
			out += "test " + n + " ... FAILED\n"
		}
		exit := 0
		if failing {
			exit = 1
		}
		r.Commands = []CmdResult{{Phase: "test", Cmd: "cargo test", Exit: exit, Output: out}}
		r.TestsRan = true
		return r
	}

	// Base: velocity fails. Fix: velocity passes, lock_order now fails —
	// lock_order passed on base, so it is a NEW regression introduced by
	// the fix; velocity no longer fails, so it is neither.
	base := mk(true, "velocity_bumps")
	fix := mk(true, "lock_order")
	newFails, preexist := CompareRepoTests(fix, base)
	if len(newFails) != 1 || newFails[0] != "lock_order" {
		t.Errorf("newFails = %v, want [lock_order]", newFails)
	}
	if len(preexist) != 0 {
		t.Errorf("preexist = %v, want none", preexist)
	}

	// Base: blank-zip fails. Fix: same blank-zip fails (same set).
	base2 := mk(true, "ShippingServiceTest.calculateQuote_rejectsBlankDestinationZip")
	fix2 := mk(true, "ShippingServiceTest.calculateQuote_rejectsBlankDestinationZip")
	newFails2, preexist2 := CompareRepoTests(fix2, base2)
	if len(newFails2) != 0 {
		t.Errorf("newFails = %v, want none", newFails2)
	}
	if len(preexist2) != 1 {
		t.Errorf("preexist = %v, want the one pre-existing failure", preexist2)
	}

	// No base run: everything is pre-existing (conservative).
	newFails3, preexist3 := CompareRepoTests(fix2, nil)
	if len(newFails3) != 0 || len(preexist3) != 1 {
		t.Errorf("nil base: new=%v pre=%v", newFails3, preexist3)
	}
}

func TestRunResultSummary(t *testing.T) {
	var nilResult *RunResult
	if nilResult.Summary() != "not run" {
		t.Error("nil summary")
	}
	r := &RunResult{TestsRan: true, Commands: []CmdResult{
		{Phase: "test", Exit: 0},
		{Phase: "test", Exit: 1},
		{Phase: "fmt", Exit: 0},
	}}
	if got := r.Summary(); got != "1 passed / 1 failed" {
		t.Errorf("summary = %q", got)
	}
}

// Run #28: user-api's CI runs `npx tsc --noEmit` (build phase) before
// `npm test`. The runner must parse build markers and compare build
// failures across branches like test failures.
func TestParseRunOutputBuildPhase(t *testing.T) {
	ci := &repoci.Discovered{
		Language:  "node",
		BuildCmds: []string{"npx tsc --noEmit"},
		TestCmds:  []string{"npm test"},
	}
	out := `cloned
===SENTINEL_CMD_START key=build-0 phase=build===
src/db.ts(9,5): error TS2451: Cannot redeclare block-scoped variable 'pool'.
===SENTINEL_CMD_END key=build-0 exit=2===
===SENTINEL_CMD_START key=test-0 phase=test===
===SENTINEL_CMD_END key=test-0 exit=0===
===SENTINEL_DONE===
`
	res := parseRunOutput(out, ci)
	if len(res.Commands) != 2 {
		t.Fatalf("commands = %+v", res.Commands)
	}
	if res.Commands[0].Phase != "build" || res.Commands[0].Exit != 2 || res.Commands[0].Cmd != "npx tsc --noEmit" {
		t.Errorf("build cmd: %+v", res.Commands[0])
	}
	if !res.BuildsRan {
		t.Error("BuildsRan should be true")
	}
	if !res.TestsRan {
		t.Error("TestsRan should be true")
	}
	if !res.HasTestFailures() {
		t.Error("build failure must count as a check failure")
	}
	if got := res.Summary(); got != "1 passed / 0 failed (build: 0 passed / 1 failed)" {
		t.Errorf("summary = %q", got)
	}
}

func TestCompareRepoTestsBuildFailure(t *testing.T) {
	mk := func(buildExit int) *RunResult {
		return &RunResult{
			Readback: map[string]string{},
			Commands: []CmdResult{
				{Phase: "build", Cmd: "npx tsc --noEmit", Exit: buildExit, Output: "src/db.ts(9,5): error TS2451"},
			},
			BuildsRan: true,
		}
	}
	// Base builds clean, fix breaks the type check — NEW failure.
	newFails, preexist := CompareRepoTests(mk(2), mk(0))
	if len(newFails) != 1 || newFails[0] != "command: npx tsc --noEmit" {
		t.Errorf("newFails = %v, want [command: npx tsc --noEmit]", newFails)
	}
	if len(preexist) != 0 {
		t.Errorf("preexist = %v, want none", preexist)
	}
	// Both branches broken (base rot) — pre-existing, not the fix's fault.
	newFails2, preexist2 := CompareRepoTests(mk(2), mk(2))
	if len(newFails2) != 0 {
		t.Errorf("newFails = %v, want none", newFails2)
	}
	if len(preexist2) != 1 {
		t.Errorf("preexist = %v, want the base-rot failure", preexist2)
	}
}

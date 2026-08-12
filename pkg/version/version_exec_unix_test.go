//go:build !windows

package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunGitCommandWithExecPreservesDirectoryAsArgv covers the direct
// Cmd.Output path. The -C directory is one argv element even when it contains
// shell metacharacters.
func TestRunGitCommandWithExecPreservesDirectoryAsArgv(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "injected")
	gitPath := filepath.Join(tempDir, "git")
	argsPath := filepath.Join(tempDir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TC_GIT_ARGS\"\nprintf 'deadbeef\\n'\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TC_GIT_ARGS", argsPath)

	dir := tempDir + ";:>" + markerPath
	got := runGitCommandWithExec(dir, "rev-parse", "HEAD")

	if got != "deadbeef" {
		t.Fatalf("expected fake git output, got %q", got)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("metacharacter payload created marker: %v", err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"-C", dir, "rev-parse", "HEAD"}, "\n") + "\n"
	if string(data) != want {
		t.Fatalf("expected argv %q, got %q", want, string(data))
	}
}

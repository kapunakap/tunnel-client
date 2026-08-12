//go:build !windows

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStartTmuxCommandDoesNotExecuteMetacharacters covers the shell boundary
// used by tmux new-session. The v0.0.11 quoting bug let a semicolon in a
// caller-supplied path start a second shell command.
func TestStartTmuxCommandDoesNotExecuteMetacharacters(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "injected")
	helperPath := writeArgvRecorder(t, tempDir)
	// Keep the payload relative so the regression does not depend on whether
	// the host's temp directory happens to contain a character that older
	// conditional quoting treated as special.
	payload := "profile-dir;:>injected"
	logPath := filepath.Join(tempDir, "runtime.log")

	var shellCommand string
	rt := Runtime{
		Run: func(args []string, _ map[string]string) (CompletedProcess, error) {
			require.Len(t, args, 6)
			require.Equal(t, []string{"tmux", "new-session", "-d", "-s", "session"}, args[:5])
			shellCommand = args[5]
			return CompletedProcess{}, nil
		},
	}

	_, err := StartTmux(rt, "session", helperPath, "profile", payload, nil, logPath)
	require.NoError(t, err)
	require.NotEmpty(t, shellCommand)

	cmd := exec.Command("/bin/sh", "-c", shellCommand)
	cmd.Dir = tempDir
	require.NoError(t, cmd.Run())

	require.NoFileExists(t, markerPath)
	require.Equal(t, strings.Join([]string{"run", "--profile-dir", payload, "--profile", "profile"}, "\n")+"\n", readFile(t, filepath.Join(tempDir, "argv")))
}

// TestStartProcessPreservesMetacharactersAsArgv covers the direct Cmd.Start
// path. The payload must reach the child as one literal argument, not shell
// syntax.
func TestStartProcessPreservesMetacharactersAsArgv(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "injected")
	helperPath := writeArgvRecorder(t, tempDir)
	payload := "profile;:>" + markerPath
	logPath := filepath.Join(tempDir, "runtime.log")

	process, err := startProcess([]string{helperPath, payload}, nil, logPath)
	require.NoError(t, err)

	osProcess, ok := process.(*osProcess)
	require.True(t, ok)
	select {
	case exitCode := <-osProcess.waitCh:
		require.Zero(t, exitCode)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for process exit")
	}

	require.NoFileExists(t, markerPath)
	require.Equal(t, payload+"\n", readFile(t, filepath.Join(tempDir, "argv")))
}

func writeArgvRecorder(t *testing.T, tempDir string) string {
	t.Helper()

	path := filepath.Join(tempDir, "record-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/argv\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

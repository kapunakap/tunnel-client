package mcpclient

import (
	"errors"
	"fmt"
	"time"
)

// StdioDiscoveryLegacyErrorCode is an implementation-defined JSON-RPC error
// code used only when the tunnel must turn a discovery timeout into a
// terminal upstream response. A downstream JSON-RPC error is never rewritten
// to this code.
const StdioDiscoveryLegacyErrorCode int64 = -32000

// StdioDiscoveryTimeoutError reports that a stdio server did not answer the
// initial server/discover probe before the compatibility timeout. It does not
// unwrap to context.DeadlineExceeded because the dispatcher must deliver this
// as a compatibility error instead of dropping it as a generic deadline.
type StdioDiscoveryTimeoutError struct {
	Timeout time.Duration
	Cause   error
}

func (e *StdioDiscoveryTimeoutError) Error() string {
	if e == nil {
		return "stdio MCP discovery timed out"
	}
	return fmt.Sprintf("stdio MCP discovery timed out after %s", e.Timeout)
}

// IsStdioDiscoveryTimeout reports whether err is the compatibility timeout
// that must be surfaced to the upstream MCP client.
func IsStdioDiscoveryTimeout(err error) bool {
	var timeoutErr *StdioDiscoveryTimeoutError
	return errors.As(err, &timeoutErr)
}

func newStdioDiscoveryTimeoutError(timeout time.Duration, cause error) error {
	return &StdioDiscoveryTimeoutError{Timeout: timeout, Cause: cause}
}

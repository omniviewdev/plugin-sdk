package exectest

import "fmt"

// CodeExitError is a stand-in for the error that client-go's executor returns when the command it ran exited with a status
// (k8s.io/client-go/util/exec.CodeExitError): its message, "command terminated with exit code N", and its methods (String, Exited,
// which is true, and ExitStatus), so that an author unit-tests the mapping of the error of their own executor without a cluster and
// without importing client-go:
//
//	exit, ok := exec.ExitOfStreamError(fmt.Errorf("stream: %w", exectest.CodeExitError{Code: 3}))
//	// exit is exec.ExitCode(3), from the process, and ok is true
//
// As client-go's is, it is a value: it is compared and wrapped as one, and errors.As finds it as one.
type CodeExitError struct{ Code int }

// Error is the message client-go gives a command that exited with a status.
func (e CodeExitError) Error() string {
	return fmt.Sprintf("command terminated with exit code %d", e.Code)
}

// String is the same message, as client-go's ExitError has it.
func (e CodeExitError) String() string { return e.Error() }

// Exited is true: the process exited, which a signal that ended it would not say.
func (CodeExitError) Exited() bool { return true }

// ExitStatus is the status the target relayed.
func (e CodeExitError) ExitStatus() int { return e.Code }

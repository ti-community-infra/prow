/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package entrypoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/prow/pkg/pod-utils/wrapper"
)

const (
	// internalCode is greater than 256 to signify entrypoint
	// chose the code rather than the command it ran
	// http://tldp.org/LDP/abs/html/exitcodes.html
	//
	// TODO(fejta): consider making all entrypoint-chosen codes internal
	internalCode = 1000
	// InternalErrorCode is what we write to the marker file to
	// indicate that we failed to start the wrapped command
	InternalErrorCode = 127
	// AbortedErrorCode is what we write to the marker file to
	// indicate that we were terminated via a signal.
	AbortedErrorCode = 130

	// PreviousErrorCode indicates a previous step failed so we
	// did not run this step.
	PreviousErrorCode = internalCode + AbortedErrorCode

	// DefaultTimeout is the default timeout for the test
	// process before SIGINT is sent
	DefaultTimeout = 120 * time.Minute

	// DefaultGracePeriod is the default timeout for the test
	// process after SIGINT is sent before SIGKILL is sent
	DefaultGracePeriod = 15 * time.Second
)

var (
	// errTimedOut is used as the command's error when the command
	// is terminated after the timeout is reached
	errTimedOut = errors.New("process timed out")
	// errAborted is used as the command's error when the command
	// is shut down by an external signal
	errAborted = errors.New("process aborted")
)

// Run executes the test process then writes the exit code to the marker file.
// This function returns the status code that should be passed to os.Exit().
func (o Options) Run() int {
	interrupt := make(chan os.Signal, 1)
	return o.internalRun(interrupt)
}

func (o Options) internalRun(interrupt chan os.Signal) int {
	code, err := o.ExecuteProcess(interrupt)
	if err != nil {
		logrus.WithError(err).Error("Error executing test process")
	}
	if err := o.Mark(code); err != nil {
		logrus.WithError(err).Error("Error writing exit code to marker file")
		return InternalErrorCode // we need to mark the real error code to safely return AlwaysZero
	}
	if o.AlwaysZero {
		return 0
	}
	return code
}

// ExecuteProcess creates the artifact directory then executes the process as
// configured, writing the output to the process log.
func (o Options) ExecuteProcess(signaledInterrupt chan os.Signal) (int, error) {
	if o.ArtifactDir != "" {
		if err := os.MkdirAll(o.ArtifactDir, os.ModePerm); err != nil {
			return InternalErrorCode, fmt.Errorf("could not create artifact directory(%s): %w", o.ArtifactDir, err)
		}
	}
	processLogFile, err := os.Create(o.ProcessLog)
	if err != nil {
		return InternalErrorCode, fmt.Errorf("could not create process logfile(%s): %w", o.ProcessLog, err)
	}
	defer processLogFile.Close()

	output := io.MultiWriter(os.Stdout, processLogFile)
	logrus.SetOutput(output)
	defer logrus.SetOutput(os.Stdout)

	// if we get asked to terminate we need to forward
	// that to the wrapped process as if it timed out
	interrupt := signaledInterrupt
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	if o.PreviousMarker != "" {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case s := <-interrupt:
				logrus.Errorf("Received interrupt %s, cancelling...", s)
				cancel()
			case <-ctx.Done():
			}
		}()
		prevMarkerResult := wrapper.WaitForMarkers(ctx, o.PreviousMarker)[o.PreviousMarker]
		code, err := prevMarkerResult.ReturnCode, prevMarkerResult.Err
		cancel() // end previous go-routine when not interrupted
		if err != nil {
			return InternalErrorCode, fmt.Errorf("wait for previous marker %s: %w", o.PreviousMarker, err)
		}
		if code != 0 {
			logrus.Infof("Skipping as previous step exited %d", code)
			return PreviousErrorCode, nil
		}
	}

	executable := o.Args[0]
	var arguments []string
	if len(o.Args) > 1 {
		arguments = o.Args[1:]
	}
	command := exec.Command(executable, arguments...)
	command.Stderr = output
	command.Stdout = output
	if err := command.Start(); err != nil {
		errs := []error{fmt.Errorf("could not start the process: %w", err)}
		if _, err := processLogFile.Write([]byte(errs[0].Error())); err != nil {
			errs = append(errs, err)
		}
		return InternalErrorCode, utilerrors.NewAggregate(errs)
	}

	timeout := optionOrDefault(o.Timeout, DefaultTimeout)
	gracePeriod := optionOrDefault(o.GracePeriod, DefaultGracePeriod)
	var commandErr error
	cancelled, aborted := false, false
	done := make(chan error)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		commandErr = err
	case <-time.After(timeout):
		logrus.Errorf("Process did not finish before %s timeout", timeout)
		cancelled = true
		gracefullyTerminate(command, done, gracePeriod, nil)
	case s := <-interrupt:
		logrus.Errorf("Entrypoint received interrupt: %v", s)
		cancelled = true
		aborted = true
		gracefullyTerminate(command, done, gracePeriod, &s)
	}

	var returnCode int
	if cancelled {
		if aborted {
			commandErr = errAborted
			if o.PropagateErrorCode {
				returnCode = command.ProcessState.ExitCode()
			} else {
				returnCode = AbortedErrorCode
			}
		} else {
			commandErr = errTimedOut
			if o.PropagateErrorCode {
				returnCode = command.ProcessState.ExitCode()
			} else {
				returnCode = InternalErrorCode
			}
		}
	} else {
		if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok {
			returnCode = status.ExitStatus()
		} else if commandErr == nil {
			returnCode = 0
		} else {
			returnCode = 1
		}

		if returnCode != 0 {
			commandErr = fmt.Errorf("wrapped process failed: %w", commandErr)
		}
	}
	return returnCode, commandErr
}

func (o *Options) Mark(exitCode int) error {
	content := []byte(strconv.Itoa(exitCode))

	// create temp file in the same directory as the desired marker file
	dir := filepath.Dir(o.MarkerFile)
	tmpDir, err := os.MkdirTemp(dir, o.ContainerName)
	if err != nil {
		return fmt.Errorf("%s: error creating temp dir: %w", o.ContainerName, err)
	}
	tempFile, err := os.CreateTemp(tmpDir, "temp-marker")
	if err != nil {
		return fmt.Errorf("could not create temp marker file in %s: %w", tmpDir, err)
	}
	// write the exit code to the tempfile, sync to disk and close
	if _, err = tempFile.Write(content); err != nil {
		return fmt.Errorf("could not write to temp marker file (%s): %w", tempFile.Name(), err)
	}
	if err = tempFile.Sync(); err != nil {
		return fmt.Errorf("could not sync temp marker file (%s): %w", tempFile.Name(), err)
	}
	tempFile.Close()
	// set desired permission bits, then rename to the desired file name
	if err = os.Chmod(tempFile.Name(), os.ModePerm); err != nil {
		return fmt.Errorf("could not chmod (%x) temp marker file (%s): %w", os.ModePerm, tempFile.Name(), err)
	}
	if err := os.Rename(tempFile.Name(), o.MarkerFile); err != nil {
		return fmt.Errorf("could not move marker file to destination path (%s): %w", o.MarkerFile, err)
	}
	return nil
}

// optionOrDefault defaults to a value if option
// is the zero value
func optionOrDefault(option, defaultValue time.Duration) time.Duration {
	if option == 0 {
		return defaultValue
	}

	return option
}

func gracefullyTerminate(command *exec.Cmd, done <-chan error, gracePeriod time.Duration, signal *os.Signal) {
	if err := command.Process.Signal(os.Interrupt); err != nil {
		logrus.WithError(err).Error("Could not interrupt process after timeout")
	}
	if signal != nil {
		if err := command.Process.Signal(*signal); err != nil {
			logrus.WithError(err).Errorf("Could not send signal %v to process after timeout", signal)
		}
	}
	select {
	case <-done:
		logrus.Errorf("Process gracefully exited before %s grace period", gracePeriod)
		// but we ignore the output error as we will want errTimedOut
	case <-time.After(gracePeriod):
		logrus.Errorf("Process did not exit before %s grace period", gracePeriod)
		if err := command.Process.Kill(); err != nil {
			logrus.WithError(err).Error("Could not kill process after grace period")
		}
	}
}

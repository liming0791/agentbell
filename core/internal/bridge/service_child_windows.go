package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runServiceChild(
	ctx context.Context,
	executable string,
	args []string,
	input []byte,
	stdout io.Writer,
	stderr io.Writer,
) error {
	job, err := createServiceJob()
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(input)
	command.Stdout = stdout
	command.Stderr = stderr
	configureBackgroundChild(command)
	if err := command.Start(); err != nil {
		return err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("open AgentBell service child for job assignment: %w", err)
	}
	assignErr := windows.AssignProcessToJobObject(job, process)
	closeErr := windows.CloseHandle(process)
	if assignErr != nil || closeErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf(
			"assign AgentBell service child to kill-on-close job: %w",
			errors.Join(assignErr, closeErr),
		)
	}
	return command.Wait()
}

func createServiceJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create AgentBell service job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("configure AgentBell service job: %w", err)
	}
	return job, nil
}

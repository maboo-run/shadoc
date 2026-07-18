//go:build windows

package main

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/windows/svc"
)

func runEntry(run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return run(context.Background())
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var runErr error
	err = svc.Run(windowsServiceNameForExecutable(executable), &serviceHandler{run: run, result: &runErr})
	return errors.Join(err, runErr)
}

type serviceHandler struct {
	run    func(context.Context) error
	result *error
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			*h.result = err
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				*h.result = <-done
				return false, 0
			}
		}
	}
}

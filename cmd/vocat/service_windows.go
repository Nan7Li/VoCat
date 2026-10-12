//go:build windows

package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"

	"vocat/internal/loghub"
)

const defaultWindowsServiceName = "Halo"

func runWindowsServiceIfNeeded(logger *slog.Logger, logs *loghub.Hub) bool {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false
	}
	serviceName := strings.TrimSpace(os.Getenv("VOCAT_WINDOWS_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = defaultWindowsServiceName
	}
	if err := svc.Run(serviceName, &windowsServiceHandler{logger: logger, logs: logs}); err != nil {
		logger.Error("Windows service failed", "error", err)
		os.Exit(1)
	}
	return true
}

type windowsServiceHandler struct {
	logger *slog.Logger
	logs   *loghub.Hub
}

func (handler *windowsServiceHandler) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	statuses chan<- svc.Status,
) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 30_000}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithContext(ctx, handler.logger, handler.logs) }()
	statuses <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				handler.logger.Error("server stopped", "error", err)
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending, WaitHint: 30_000}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						handler.logger.Error("server shutdown failed", "error", err)
						return false, 1
					}
					return false, 0
				case <-time.After(30 * time.Second):
					handler.logger.Error("server shutdown timed out")
					return false, 1
				}
			}
		}
	}
}

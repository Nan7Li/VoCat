package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"vocat/internal/cellbridge/cellular"
	"vocat/internal/i18n"
	"vocat/internal/vowifi"
)

const callDeferMarker = "已推迟安装"
const callDeferMarkerEN = "postponed until the call ends"

type callActiveError struct{}

func (callActiveError) Error() string {
	return i18n.T("通话进行中或状态未确认，已推迟安装，请稍后重试")
}

type updateInProgressError struct{}

func (updateInProgressError) Error() string {
	return i18n.T("软件更新正在进行，请稍后再拨号或接听")
}

var (
	errCallActive       error = callActiveError{}
	errUpdateInProgress error = updateInProgressError{}
	errUpdateBusy             = errors.New("another update is already in progress")
)

func autoUpdateWaitingForCall(message string) bool {
	return strings.Contains(message, callDeferMarker) || strings.Contains(message, callDeferMarkerEN)
}

func errCallActiveNow() error { return errCallActive }

// beginUpdate rejects installation while a call is active and publishes the
// in-progress flag under the same lock that admits dial, answer, and media.
// Controllers are snapshotted without holding updateMu or cellBridgeMu.
func (s *Server) beginUpdate() error {
	if s.callActivity() {
		return errCallActiveNow()
	}
	s.updateMu.Lock()
	if s.updateApplying {
		s.updateMu.Unlock()
		return errUpdateBusy
	}
	if s.updateCallOps > 0 {
		s.updateMu.Unlock()
		return errCallActiveNow()
	}
	s.updateApplying = true
	s.updateMu.Unlock()
	if s.callOpsBusy() || s.callActivity() {
		s.finishUpdate()
		return errCallActiveNow()
	}
	return nil
}

func (s *Server) finishUpdate() {
	if s == nil {
		return
	}
	s.updateMu.Lock()
	s.updateApplying = false
	s.updateMu.Unlock()
}

// Keep call admission closed while the old process is waiting for systemd to
// stop it. A successful --no-block restart only queues that stop; it must not
// reopen admission in this process. Failed restart requests restore admission
// and leave a visible error so the administrator can restart manually.
func (s *Server) scheduleUpdateRestart() bool {
	if s.updateRestart == nil {
		return false
	}
	restart, logger := s.updateRestart, s.logger
	go func() {
		time.Sleep(time.Second)
		if err := restart(logger); err != nil {
			s.finishUpdate()
			if logger != nil {
				logger.Error("restart after update failed", "error", err)
			}
			if s.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if settings, loadErr := s.loadAutoUpdateSettings(ctx); loadErr == nil {
					settings.LastError = i18n.T("程序已更新，但服务重启失败，请手动重启服务。") + " " + err.Error()
					settings.LastCheckAt = time.Now().UTC().Format(time.RFC3339)
					_ = s.saveAutoUpdateSettings(ctx, settings)
				}
			}
		}
	}()
	return true
}

func (s *Server) callOpsBusy() bool {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	return s.updateCallOps > 0
}

// admitCallMutation reserves a dial, answer, or media open so an update cannot
// pass its call check in the gap before the call is visible.
func (s *Server) admitCallMutation() (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateApplying {
		return nil, errUpdateInProgress
	}
	s.updateCallOps++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.updateMu.Lock()
			if s.updateCallOps > 0 {
				s.updateCallOps--
			}
			s.updateMu.Unlock()
		})
	}, nil
}

func (s *Server) rejectInstallIfCallActive() error {
	if s.callOpsBusy() || s.callActivity() {
		return errCallActiveNow()
	}
	return nil
}

func (s *Server) callActivity() bool {
	if s == nil {
		return false
	}
	if s.mediaLeaseActive() {
		return true
	}
	for _, call := range s.listCellularCalls() {
		if callInProgress(call) {
			return true
		}
	}
	calls, err := s.listIMSCalls()
	if err != nil {
		return true
	}
	for _, call := range calls {
		if callInProgress(call) {
			return true
		}
	}
	return s.legacyCallActivity()
}

func callInProgress(call vowifi.Call) bool {
	if call.EndedAt != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(call.State)) {
	case "ended", "failed", "idle":
		return false
	default:
		return true
	}
}

func (s *Server) mediaLeaseActive() bool {
	s.callMediaLeaseMu.Lock()
	defer s.callMediaLeaseMu.Unlock()
	return len(s.callMediaLeases) > 0
}

func (s *Server) listCellularCalls() []vowifi.Call {
	if s.cellularCallsForUpdate != nil {
		return s.cellularCallsForUpdate()
	}
	controller := s.snapshotCellularController()
	if controller == nil {
		return nil
	}
	return controller.Calls()
}

func (s *Server) snapshotCellularController() *cellular.Controller {
	s.cellBridgeMu.Lock()
	defer s.cellBridgeMu.Unlock()
	if s.cellBridgeCtrl == nil {
		return nil
	}
	return s.cellBridgeCtrl.controller
}

func (s *Server) listIMSCalls() ([]vowifi.Call, error) {
	if s.imsCallsForUpdate != nil {
		return s.imsCallsForUpdate()
	}
	controller, ok := s.vowifi.(VoWiFiCallController)
	if !ok || s.store == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	var calls []vowifi.Call
	for _, device := range devices {
		listed, listErr := controller.Calls(device.ID)
		if listErr != nil {
			continue
		}
		calls = append(calls, listed...)
	}
	return calls, nil
}

func (s *Server) backupDatabaseBeforeUpdate(ctx context.Context) error {
	if s.store == nil {
		return errors.New("update: database is not configured")
	}
	path, err := s.store.BackupForUpdate(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("database backup before update failed", "error", err)
		}
		return errors.New(i18n.T("数据库备份失败，已取消安装，当前程序未替换。"))
	}
	if path != "" && s.logger != nil {
		s.logger.Info("database backed up before update", "path", path)
	}
	return nil
}

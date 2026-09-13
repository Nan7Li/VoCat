//go:build windows

package device

import (
	"context"
	"fmt"

	"vocat/internal/modem"
)

func openQMIDataSession(context.Context, string) (qmiDataSession, error) {
	return nil, fmt.Errorf("%w: Windows uses the native Mobile Broadband backend instead of QMI", ErrDataBackendUnavailable)
}

func invalidateQMINetworkSession(*managedDevice, modem.Candidate) {}

func (manager *Manager) NetworkStatus(ctx context.Context, id string) (NetworkStatus, error) {
	state, err := manager.lookup(id)
	if err != nil {
		return NetworkStatus{}, err
	}
	state.dataMu.Lock()
	defer state.dataMu.Unlock()
	if err := manager.validateActive(id, state); err != nil {
		return NetworkStatus{}, err
	}
	return windowsNetworkStatus(ctx, manager.candidateFor(state))
}

func (manager *Manager) setQMINetwork(
	context.Context,
	*managedDevice,
	modem.Candidate,
	bool,
	string,
	string,
	string,
	string,
	string,
) (NetworkResult, error) {
	return NetworkResult{}, fmt.Errorf("%w: Windows uses the native Mobile Broadband backend instead of QMI", ErrDataBackendUnavailable)
}

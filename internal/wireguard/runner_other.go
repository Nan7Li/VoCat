//go:build !linux

package wireguard

import "context"

type unavailableRunner struct{}

func defaultRunner() Runner {
	return unavailableRunner{}
}

func (unavailableRunner) Available() bool { return false }

func (unavailableRunner) List(context.Context) ([]string, error) { return nil, nil }

func (unavailableRunner) Up(context.Context, string, string) error {
	return ErrAvailable
}

func (unavailableRunner) Down(context.Context, string, string) error {
	return ErrAvailable
}

func (unavailableRunner) Show(context.Context, string) (InterfaceInfo, error) {
	return InterfaceInfo{}, nil
}

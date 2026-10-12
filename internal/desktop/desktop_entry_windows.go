//go:build windows

package desktop

// Run is the native Windows entry point. It creates a Win32/GDI workspace and
// starts the sibling Go core engine when no local instance is already healthy.
func Run() error {
	return runNativeWorkspace()
}

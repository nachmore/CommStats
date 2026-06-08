//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

func Current() Provider { return darwinProvider{} }

type darwinProvider struct{}

func (darwinProvider) Secrets() Secrets     { return darwinSecrets{} }
func (darwinProvider) Paths() Paths         { return darwinPaths{} }
func (darwinProvider) Scheduler() Scheduler { return darwinScheduler{} }
func (darwinProvider) Browser() Browser     { return darwinBrowser{} }

type darwinBrowser struct{}

func (darwinBrowser) Open(target string) error {
	return exec.Command("open", target).Start()
}

func (darwinBrowser) ExecutablePath() (string, error) {
	return firstExisting([]string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	})
}

type darwinPaths struct{}

func (darwinPaths) ConfigDir() string { return appBase() }
func (darwinPaths) DataDir() string   { return filepath.Join(appBase(), "data") }

func appBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "commstats")
}

// TODO: back with macOS Keychain via the `security` CLI or Security.framework.
type darwinSecrets struct{}

func (darwinSecrets) Get(service, account string) (string, error) {
	return "", errNotImplemented("darwin Secrets.Get")
}
func (darwinSecrets) Set(service, account, secret string) error {
	return errNotImplemented("darwin Secrets.Set")
}
func (darwinSecrets) Delete(service, account string) error {
	return errNotImplemented("darwin Secrets.Delete")
}

// TODO: back with launchd (write a LaunchAgent plist + launchctl load).
type darwinScheduler struct{}

func (darwinScheduler) Install(job ScheduledJob) error {
	return errNotImplemented("darwin Scheduler.Install")
}
func (darwinScheduler) Uninstall(name string) error {
	return errNotImplemented("darwin Scheduler.Uninstall")
}

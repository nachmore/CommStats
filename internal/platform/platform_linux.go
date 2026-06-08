//go:build linux

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

func Current() Provider { return linuxProvider{} }

type linuxProvider struct{}

func (linuxProvider) Secrets() Secrets     { return linuxSecrets{} }
func (linuxProvider) Paths() Paths         { return linuxPaths{} }
func (linuxProvider) Scheduler() Scheduler { return linuxScheduler{} }
func (linuxProvider) Browser() Browser     { return linuxBrowser{} }

type linuxBrowser struct{}

func (linuxBrowser) Open(target string) error {
	return exec.Command("xdg-open", target).Start()
}

func (linuxBrowser) ExecutablePath() (string, error) {
	// Prefer PATH lookups, then common absolute locations.
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return firstExisting([]string{
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/microsoft-edge",
	})
}

type linuxPaths struct{}

func (linuxPaths) ConfigDir() string { return appBase() }
func (linuxPaths) DataDir() string   { return filepath.Join(appBase(), "data") }

// appBase honors XDG_CONFIG_HOME, falling back to ~/.config.
func appBase() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "commstats")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "commstats")
}

// TODO: back with libsecret/kwallet via the Secret Service DBus API.
type linuxSecrets struct{}

func (linuxSecrets) Get(service, account string) (string, error) {
	return "", errNotImplemented("linux Secrets.Get")
}
func (linuxSecrets) Set(service, account, secret string) error {
	return errNotImplemented("linux Secrets.Set")
}
func (linuxSecrets) Delete(service, account string) error {
	return errNotImplemented("linux Secrets.Delete")
}

// TODO: back with systemd-timer (user unit) or cron.
type linuxScheduler struct{}

func (linuxScheduler) Install(job ScheduledJob) error {
	return errNotImplemented("linux Scheduler.Install")
}
func (linuxScheduler) Uninstall(name string) error {
	return errNotImplemented("linux Scheduler.Uninstall")
}

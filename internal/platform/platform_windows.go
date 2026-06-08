//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

func Current() Provider { return windowsProvider{} }

type windowsProvider struct{}

func (windowsProvider) Secrets() Secrets     { return windowsSecrets{} }
func (windowsProvider) Paths() Paths         { return windowsPaths{} }
func (windowsProvider) Scheduler() Scheduler { return windowsScheduler{} }
func (windowsProvider) Browser() Browser     { return windowsBrowser{} }

type windowsBrowser struct{}

// Open launches target in the default handler. We use rundll32's
// FileProtocolHandler rather than `cmd /c start` because cmd treats "&" as a
// command separator and mangles URLs with query strings (OAuth authorize URLs
// are full of "&"); rundll32 receives the URL as a single argument untouched.
func (windowsBrowser) Open(target string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
}

// ExecutablePath prefers Edge (present on all modern Windows), then Chrome.
func (windowsBrowser) ExecutablePath() (string, error) {
	candidates := []string{}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	return firstExisting(candidates)
}

type windowsPaths struct{}

func (windowsPaths) ConfigDir() string { return appBase() }
func (windowsPaths) DataDir() string   { return filepath.Join(appBase(), "data") }

// appBase returns %APPDATA%\commstats, falling back to the user home.
func appBase() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "commstats")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "commstats")
}

// TODO: back with Windows Credential Manager (wincred) via golang.org/x/sys.
type windowsSecrets struct{}

func (windowsSecrets) Get(service, account string) (string, error) {
	return "", errNotImplemented("windows Secrets.Get")
}
func (windowsSecrets) Set(service, account, secret string) error {
	return errNotImplemented("windows Secrets.Set")
}
func (windowsSecrets) Delete(service, account string) error {
	return errNotImplemented("windows Secrets.Delete")
}

// TODO: back with Task Scheduler via schtasks.exe or the COM API.
type windowsScheduler struct{}

func (windowsScheduler) Install(job ScheduledJob) error {
	return errNotImplemented("windows Scheduler.Install")
}
func (windowsScheduler) Uninstall(name string) error {
	return errNotImplemented("windows Scheduler.Uninstall")
}

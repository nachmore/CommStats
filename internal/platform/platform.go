// Package platform is the OS abstraction seam. It exposes OS-specific services
// (secret storage, config/data paths, scheduling) behind interfaces. Concrete
// implementations live in build-tag-gated files (platform_darwin.go,
// platform_windows.go, platform_linux.go) and are selected at compile time, so
// only one OS's code is ever built into a binary.
package platform

// Secrets stores and retrieves credentials in the OS-native secret store
// (Keychain, Windows Credential Manager, libsecret).
type Secrets interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

// Paths resolves OS-appropriate locations for config and data.
type Paths interface {
	ConfigDir() string // e.g. ~/Library/Application Support, %APPDATA%, ~/.config
	DataDir() string   // where the SQLite file lives
}

// ScheduledJob describes a recurring invocation to register with the OS.
type ScheduledJob struct {
	Name     string   // unique identifier for the scheduled task
	Command  string   // absolute path to the commstats binary
	Args     []string // e.g. ["collect"]
	Interval string   // human interval, e.g. "1h"; impls translate per-OS
}

// Scheduler installs/removes recurring jobs via the native scheduler
// (launchd, Task Scheduler, systemd-timer/cron).
type Scheduler interface {
	Install(job ScheduledJob) error
	Uninstall(name string) error
}

// Browser locates a Chromium-family browser to drive for interactive logins.
// Discovery is OS-specific (Edge on Windows, Chrome/Edge elsewhere), so it
// belongs behind the platform seam.
type Browser interface {
	// ExecutablePath returns the path to an installed Chromium-based browser,
	// or an error if none is found.
	ExecutablePath() (string, error)
	// Open opens a file or URL in the user's default handler.
	Open(target string) error
}

// Provider bundles the platform services for the host OS. Current returns the
// implementation selected by build tags.
type Provider interface {
	Secrets() Secrets
	Paths() Paths
	Scheduler() Scheduler
	Browser() Browser
}

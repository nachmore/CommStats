package platform

import (
	"fmt"
	"os"
)

// errNotImplemented marks a platform capability whose native backing is not yet
// wired. Callers can surface this clearly rather than failing silently.
func errNotImplemented(what string) error {
	return fmt.Errorf("platform: %s not implemented yet", what)
}

// firstExisting returns the first path that exists on disk, or an error listing
// what was tried.
func firstExisting(paths []string) (string, error) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("platform: no browser found (tried %d locations)", len(paths))
}

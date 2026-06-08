package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nachmore/commstats/internal/platform"
)

// errNoConfig signals that Slack hasn't been configured yet. The collector
// treats this as "skip me", not a hard failure, so an unconfigured source
// doesn't break a multi-source collection run.
var errNoConfig = errors.New("slack: not configured")

// configFile is the on-disk shape of <ConfigDir>/slack.json.
type configFile struct {
	Token        string `json:"token"`
	Cookie       string `json:"cookie"`
	WorkspaceURL string `json:"workspace_url"`
}

// configPath returns the absolute path to the Slack credential file.
func configPath() string {
	return filepath.Join(platform.Current().Paths().ConfigDir(), "slack.json")
}

// loadCredentials reads Slack credentials from <ConfigDir>/slack.json.
// Returns errNoConfig if the file is absent.
func loadCredentials() (Credentials, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, errNoConfig
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("slack: read config: %w", err)
	}
	var cf configFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return Credentials{}, fmt.Errorf("slack: parse %s: %w", path, err)
	}
	if cf.Token == "" || cf.Cookie == "" {
		return Credentials{}, fmt.Errorf("slack: %s missing token or cookie", path)
	}
	return Credentials{
		Token:        cf.Token,
		Cookie:       cf.Cookie,
		WorkspaceURL: cf.WorkspaceURL,
	}, nil
}

// loadOrLogin returns saved credentials, or runs the interactive browser login
// (and persists the result) when none are stored.
func loadOrLogin(ctx context.Context) (Credentials, error) {
	creds, err := loadCredentials()
	if err == nil {
		return creds, nil
	}
	if err != errNoConfig {
		return Credentials{}, err
	}
	creds, err = login(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if err := saveCredentials(creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

// saveCredentials writes credentials to <ConfigDir>/slack.json with 0600
// permissions, since the token + cookie grant full access to the workspace.
func saveCredentials(c Credentials) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("slack: create config dir: %w", err)
	}
	data, err := json.MarshalIndent(configFile{
		Token:        c.Token,
		Cookie:       c.Cookie,
		WorkspaceURL: c.WorkspaceURL,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("slack: write config: %w", err)
	}
	return nil
}

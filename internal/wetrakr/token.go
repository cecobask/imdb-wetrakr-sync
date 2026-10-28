package wetrakr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoToken means no token is stored yet: the user must run the auth command first.
var ErrNoToken = errors.New("no WeTrakr token found: run `iws auth` first")

// TokenStore keeps the OAuth tokens in a JSON file.
type TokenStore struct {
	path string
}

func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

func (s *TokenStore) Path() string {
	return s.path
}

// Load reads the token file. It returns ErrNoToken when the file is missing or empty.
func (s *TokenStore) Load() (*Token, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(strings.TrimSpace(string(data))) == 0) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, fmt.Errorf("failure reading token file %s: %w", s.path, err)
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("%w: the token file %s is not valid JSON: %v", ErrNoToken, s.path, err)
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		return nil, ErrNoToken
	}
	return &t, nil
}

// Save writes the token file atomically, so a crash never leaves a half-written file and loses the rotated refresh
// token.
func (s *TokenStore) Save(t *Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".wetrakr-token-*.json")
	if err != nil {
		return fmt.Errorf("failure creating temporary token file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("failure writing token file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failure setting token file permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failure writing token file: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("failure replacing token file %s: %w", s.path, err)
	}
	return nil
}

// expiry returns when the access token expires. It falls back to the exp claim of the JWT when the stored token has no
// expiry, and returns the zero time when neither is known.
func (t *Token) expiry() time.Time {
	if t.ExpiresAt > 0 {
		return time.Unix(t.ExpiresAt, 0)
	}
	parts := strings.Split(t.AccessToken, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// Delete removes the token file. A missing file is not an error.
func (s *TokenStore) Delete() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failure removing token file %s: %w", s.path, err)
	}
	return nil
}

package identity

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultFileName = "user-id"
const defaultTokenFileName = "token.json"

var userIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

var ErrTokenNotFound = errors.New("token is not configured")

type TokenRecord struct {
	Token       string `json:"token"`
	InstalledAt int64  `json:"installed_at"`
}

// LoadOrCreate returns the stable user ID associated with this installation.
// When path is empty, the ID is stored in the operating system's user config
// directory under incidentctl/user-id.
func LoadOrCreate(path string) (string, error) {
	path, err := resolveUserIDPath(path)
	if err != nil {
		return "", err
	}

	if id, err := read(path); err == nil {
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create user ID directory: %w", err)
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		if existingID, readErr := read(path); readErr == nil {
			return existingID, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("create user ID file: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(id + "\n"); err != nil {
		return "", fmt.Errorf("write user ID file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("protect user ID file: %w", err)
	}
	return id, nil
}

func ResolveTokenPath(path string) (string, error) {
	return resolvePath(path, defaultTokenFileName)
}

func LoadToken(path string) (TokenRecord, error) {
	path, err := ResolveTokenPath(path)
	if err != nil {
		return TokenRecord{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TokenRecord{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenRecord{}, fmt.Errorf("read token file: %w", err)
	}
	var record TokenRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return TokenRecord{}, fmt.Errorf("decode token file: %w", err)
	}
	if strings.TrimSpace(record.Token) == "" || strings.ContainsAny(record.Token, "\r\n") {
		return TokenRecord{}, fmt.Errorf("token file %s contains an invalid token", path)
	}
	if record.InstalledAt <= 0 {
		return TokenRecord{}, fmt.Errorf("token file %s contains an invalid installation date", path)
	}
	return record, nil
}

func SaveToken(path, token string) (TokenRecord, error) {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return TokenRecord{}, errors.New("token must not be empty or contain line breaks")
	}
	path, err := ResolveTokenPath(path)
	if err != nil {
		return TokenRecord{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return TokenRecord{}, fmt.Errorf("create token directory: %w", err)
	}
	record := TokenRecord{Token: token, InstalledAt: time.Now().Unix()}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return TokenRecord{}, fmt.Errorf("encode token file: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return TokenRecord{}, fmt.Errorf("create temporary token file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return TokenRecord{}, fmt.Errorf("protect temporary token file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return TokenRecord{}, fmt.Errorf("write token file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return TokenRecord{}, fmt.Errorf("close temporary token file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return TokenRecord{}, fmt.Errorf("install token file: %w", err)
	}
	return record, nil
}

func resolveUserIDPath(path string) (string, error) {
	return resolvePath(path, defaultFileName)
}

func resolvePath(path, defaultName string) (string, error) {
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config directory: %w", err)
		}
		return filepath.Join(configDir, "incidentctl", defaultName), nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			return filepath.Join(home, defaultName), nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !userIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid user ID in %s", path)
	}
	return id, nil
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate user ID: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

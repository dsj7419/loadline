package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// tokenBytes is the amount of randomness in an ingest token.
const tokenBytes = 32

// EnsureToken returns the ingest token stored at path, creating it first when
// the file does not exist. The token is tokenBytes random bytes, hex-encoded.
// A file that holds anything else is an error, never silently replaced,
// because replacing it would lock out every hook that already holds the old
// token.
//
// It is safe to call from several processes at once: the token is written to
// a private temporary file and then linked into place, which fails if the
// file already exists, so a reader never sees a partly written token and
// every caller ends up with the same value.
func EnsureToken(path string) ([]byte, error) {
	token, err := readToken(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create token directory: %w", err)
	}
	return createToken(path)
}

// ReadToken returns the ingest token stored at path, holding it to the same
// rule as EnsureToken, but never creates one: a missing file is an error
// that matches fs.ErrNotExist.
func ReadToken(path string) ([]byte, error) {
	return readToken(path)
}

func readToken(path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}

	token := bytes.TrimSpace(data)
	if raw, err := hex.DecodeString(string(token)); err != nil || len(raw) != tokenBytes {
		return nil, fmt.Errorf("token file %s does not hold %d hex characters; delete it to generate a new token",
			path, 2*tokenBytes)
	}
	return token, nil
}

// createToken publishes a new token at path, or returns the token another
// process published first.
func createToken(path string) ([]byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := []byte(hex.EncodeToString(raw))

	tempPath, err := writeTempFile(filepath.Dir(path), token)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Best effort. The token is already published by this call or by the
		// process that won, so a leftover name only costs tidiness.
		_ = os.Remove(tempPath)
	}()

	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readToken(path)
		}
		return nil, fmt.Errorf("publish token file: %w", err)
	}
	return token, nil
}

// writeTempFile writes content to a new user-only file in dir and returns its
// path.
func writeTempFile(dir string, content []byte) (string, error) {
	file, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return "", fmt.Errorf("create temporary token file: %w", err)
	}

	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("write temporary token file: %w", err)
	}
	return file.Name(), nil
}

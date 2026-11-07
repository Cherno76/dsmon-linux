package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	secretPrefix = "v1:"
	secretAAD    = "dsmon-config-api-key"
)

func loadOrCreateKey(path string) ([]byte, error) {
	if key, err := loadKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create encryption key directory: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read concurrent encryption key: %w", readErr)
		}
		return decodeKey(data)
	}
	if err != nil {
		return nil, fmt.Errorf("create encryption key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw) + "\n"
	if _, err := file.WriteString(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write encryption key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync encryption key: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close encryption key: %w", err)
	}
	return raw, nil
}

func loadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read encryption key: %w", err)
	}
	return decodeKey(data)
}

func decodeKey(data []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(raw))
	}
	return raw, nil
}

func encryptSecret(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create AES-GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(secretAAD))
	payload := append(nonce, ciphertext...)
	return secretPrefix + base64.StdEncoding.EncodeToString(payload), nil
}

func decryptSecret(key []byte, encoded string) (string, error) {
	if !strings.HasPrefix(encoded, secretPrefix) {
		return "", errors.New("unsupported encrypted secret format")
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create AES-GCM: %w", err)
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("encrypted secret is truncated")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(secretAAD))
	if err != nil {
		return "", errors.New("decrypt API key: authentication failed")
	}
	return string(plaintext), nil
}

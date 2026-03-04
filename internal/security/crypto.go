package security

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	saltSize     = 16
	nonceSize    = 12
	keySize      = 32 // AES-256
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
)

type Encryptor struct {
	key []byte
}

func NewEncryptorFromPassphrase(passphrase string) (*Encryptor, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, errors.New("master key passphrase is required")
	}
	// Key material derived from passphrase and per-record salt during encryption.
	return &Encryptor{key: []byte(passphrase)}, nil
}

func PromptOrReadMasterKey() (string, error) {
	if key, ok := os.LookupEnv("VIADUCT_MASTER_KEY"); ok && strings.TrimSpace(key) != "" {
		return key, nil
	}
	fmt.Fprint(os.Stdout, "Enter VIADUCT master key passphrase: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("master key passphrase cannot be empty")
	}
	return line, nil
}

func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("plaintext is empty")
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	key := deriveKey(e.key, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, saltSize+nonceSize+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

func (e *Encryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < saltSize+nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	salt := ciphertext[:saltSize]
	nonce := ciphertext[saltSize : saltSize+nonceSize]
	payload := ciphertext[saltSize+nonceSize:]
	key := deriveKey(e.key, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

func deriveKey(passphrase, salt []byte) []byte {
	return argon2.IDKey(passphrase, salt, argonTime, argonMemory, argonThreads, keySize)
}

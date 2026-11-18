package dbeaver

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// dbeaverKey is the AES key DBeaver CE encrypts credentials-config.json
// with, LOCAL_KEY_CACHE in BaseProjectImpl.java.
var dbeaverKey = []byte{0xba, 0xbb, 0x4a, 0x9f, 0x77, 0x4a, 0xb8, 0x53, 0xc9, 0x6c, 0x2d, 0x65, 0x3d, 0xfe, 0x54, 0x4a}

// credentials are the decrypted secrets of a credentials file, by
// connection id, then section ("#connection", "network/ssh_tunnel"), then
// key.
type credentials map[string]map[string]map[string]string

// get is one secret, "" when missing.
func (c credentials) get(id, section, key string) string {
	return c[id][section][key]
}

// decrypt reads a file DBeaver encrypted: a 16-byte IV, then AES-CBC
// ciphertext with PKCS#5 padding.
func decrypt(data []byte) ([]byte, error) {
	if len(data) < 2*aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("not encrypted the way DBeaver does: wrong length")
	}
	block, err := aes.NewCipher(dbeaverKey)
	if err != nil {
		return nil, err
	}
	iv, body := data[:aes.BlockSize], data[aes.BlockSize:]
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, body)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize {
		return nil, errors.New("not encrypted with DBeaver's key: bad padding")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, errors.New("not encrypted with DBeaver's key: bad padding")
		}
	}
	return plain[:len(plain)-pad], nil
}

// readCredentials decrypts a credentials file; a missing one has none.
func readCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw map[string]map[string]map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	creds := credentials{}
	for id, sections := range raw {
		creds[id] = map[string]map[string]string{}
		for section, values := range sections {
			creds[id][section] = map[string]string{}
			for k, v := range values {
				creds[id][section][k] = text(v)
			}
		}
	}
	return creds, nil
}

package bitwarden

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// key is an AES-256-CBC key followed by an HMAC-SHA256 key, the layout of
// Bitwarden's symmetric keys.
type key [64]byte

var (
	errCipherType = errors.New("unsupported cipher type")
	errCipherText = errors.New("malformed ciphertext")
	errCipherMAC  = errors.New("ciphertext failed its integrity check")
)

// deriveShareableKey is Bitwarden's derive_shareable_key: HKDF-Expand over
// HMAC-SHA256("bitwarden-<name>", secret).
func deriveShareableKey(secret []byte, name, info string) key {
	mac := hmac.New(sha256.New, []byte("bitwarden-"+name))
	mac.Write(secret)
	expanded, err := hkdf.Expand(sha256.New, mac.Sum(nil), info, len(key{}))
	if err != nil {
		panic(err) // the lengths are constants HKDF accepts
	}
	return key(expanded)
}

// encrypt returns plaintext as a type 2 EncString, "2.<iv>|<data>|<mac>":
// AES-256-CBC with PKCS#7 padding, then HMAC-SHA256 over the IV and data.
func (k *key) encrypt(plaintext []byte) string {
	var iv [aes.BlockSize]byte
	rand.Read(iv[:])
	return k.encryptWith(iv, plaintext)
}

func (k *key) encryptWith(iv [aes.BlockSize]byte, plaintext []byte) string {
	block, err := aes.NewCipher(k[:32])
	if err != nil {
		panic(err) // the key length is fixed
	}
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	data := make([]byte, len(plaintext)+pad)
	copy(data, plaintext)
	for i := len(plaintext); i < len(data); i++ {
		data[i] = byte(pad)
	}
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(data, data)
	encode := base64.StdEncoding.EncodeToString
	return "2." + encode(iv[:]) + "|" + encode(data) + "|" + encode(k.mac(iv[:], data))
}

func (k *key) mac(iv, data []byte) []byte {
	mac := hmac.New(sha256.New, k[32:])
	mac.Write(iv)
	mac.Write(data)
	return mac.Sum(nil)
}

// decrypt opens a type 2 EncString. It checks the MAC before it decrypts,
// and reports any other cipher type as unsupported rather than guessing.
func (k *key) decrypt(text string) ([]byte, error) {
	kind, rest, ok := strings.Cut(text, ".")
	if !ok || kind != "2" {
		return nil, errCipherType
	}
	parts := strings.Split(rest, "|")
	if len(parts) != 3 {
		return nil, errCipherText
	}
	var fields [3][]byte
	for i, part := range parts {
		field, err := base64.StdEncoding.DecodeString(part)
		if err != nil {
			return nil, errCipherText
		}
		fields[i] = field
	}
	iv, data, mac := fields[0], fields[1], fields[2]
	if len(iv) != aes.BlockSize || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errCipherText
	}
	if !hmac.Equal(mac, k.mac(iv, data)) {
		return nil, errCipherMAC
	}
	block, err := aes.NewCipher(k[:32])
	if err != nil {
		panic(err) // the key length is fixed
	}
	plaintext := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, data)
	pad := int(plaintext[len(plaintext)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errCipherText
	}
	for _, b := range plaintext[len(plaintext)-pad:] {
		if int(b) != pad {
			return nil, errCipherText
		}
	}
	return plaintext[:len(plaintext)-pad], nil
}

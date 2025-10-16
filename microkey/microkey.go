package microkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
)

// GenerateKeyPair creates a new ed25519 keypair and returns base64-encoded
// privateKey and publicKey (separately). The private key is the 64-byte
// ed25519 private key blob encoded in base64.
func GenerateKeyPair() (privB64 string, pubB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	privB64 = base64.StdEncoding.EncodeToString(priv)
	pubB64 = base64.StdEncoding.EncodeToString(pub)
	return privB64, pubB64, nil
}

// SignMessage signs arbitrary data (message) using a base64-encoded private key.
// Returns the signature encoded as base64.
func SignMessage(privB64 string, message []byte) (sigB64 string, err error) {
	privBytes, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		return "", err
	}
	if l := len(privBytes); l != ed25519.PrivateKeySize {
		return "", errors.New("invalid private key length")
	}
	sig := ed25519.Sign(ed25519.PrivateKey(privBytes), message)
	sigB64 = base64.StdEncoding.EncodeToString(sig)
	return sigB64, nil
}

// VerifyMessage verifies a base64-encoded signature against the message using
// a base64-encoded public key. Returns true if valid.
func VerifyMessage(pubB64 string, message []byte, sigB64 string) (bool, error) {
	pubBytes, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return false, err
	}
	if l := len(pubBytes); l != ed25519.PublicKeySize {
		return false, errors.New("invalid public key length")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false, err
	}
	if l := len(sigBytes); l != ed25519.SignatureSize {
		return false, errors.New("invalid signature length")
	}
	ok := ed25519.Verify(ed25519.PublicKey(pubBytes), message, sigBytes)
	return ok, nil
}

// SignPublicKey signs another party's public key. Accepts signer's private key
// (base64) and the subject public key (base64). Returns signature base64.
// The message that is signed is the raw bytes of the subject public key (not
// the base64 string). This makes signatures robust and unambiguous.
func SignPublicKey(signerPrivB64 string, subjectPubB64 string) (sigB64 string, err error) {
	subjectPubBytes, err := base64.StdEncoding.DecodeString(subjectPubB64)
	if err != nil {
		return "", err
	}
	return SignMessage(signerPrivB64, subjectPubBytes)
}

// VerifyPublicKeySignature verifies that signerPubB64 (the signer's public key)
// produced sigB64 over the subject public key subjectPubB64. Returns true if
// signature verifies.
func VerifyPublicKeySignature(signerPubB64 string, subjectPubB64 string, sigB64 string) (bool, error) {
	subjectPubBytes, err := base64.StdEncoding.DecodeString(subjectPubB64)
	if err != nil {
		return false, err
	}
	return VerifyMessage(signerPubB64, subjectPubBytes, sigB64)
}

// Convenience helpers to save/read base64 strings to/from files. These are tiny
// wrappers; errors are returned for caller handling.

// SaveBase64File writes the given base64 string to a file (no newline appended).
func SaveBase64File(path string, base64Content string) error {
	return os.WriteFile(path, []byte(base64Content), 0600)
}

// ReadBase64File reads the whole file and returns its contents as a string.
func ReadBase64File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ReadBase64Files(path ...string) ([]string, error) {
	var b64s []string
	for _, path := range path {
		b64, err := ReadBase64File(path)
		if err != nil {
			return nil, err
		}
		b64s = append(b64s, b64)
	}
	return b64s, nil
}

// GetKeypair reads a base64-encoded private key and a base64-encoded public key from
// two files (privPath and pubPath). If either file does not exist, it generates
// a new keypair and saves it to the files. Returns base64-encoded private key and
// public key (separately).
func GetKeypair(privPath string, pubPath string) (privB64 string, pubB64 string, err error) {
	keys, err := ReadBase64Files(privPath, pubPath)
	if err != nil {
		privB64, pubB64, err = GenerateKeyPair()
		if err != nil {
			return "", "", err
		}
		if err = SaveBase64File(privPath, privB64); err != nil {
			return "", "", err
		}
		if err = SaveBase64File(pubPath, pubB64); err != nil {
			return "", "", err
		}
	} else {
		privB64, pubB64 = keys[0], keys[1]
	}
	return privB64, pubB64, nil
}

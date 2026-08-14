package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/gowebpki/jcs"
)

const (
	APIVersion                  = "dagrail.io/v1alpha1"
	Kind                        = "DetachedSignature"
	Algorithm                   = "Ed25519"
	Domain                      = "dagrail-detached-signature-v1\x00"
	keyLimit                    = 64 * 1024
	MaxSignedPayloadBytes int64 = 1 << 30
)

type Envelope struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	Algorithm     string `json:"algorithm"`
	KeyID         string `json:"keyId"`
	PayloadSHA256 string `json:"payloadSha256"`
	Signature     string `json:"signature"`
}

type Report struct {
	Valid         bool   `json:"valid"`
	KeyID         string `json:"keyId"`
	PayloadSHA256 string `json:"payloadSha256,omitempty"`
	SignaturePath string `json:"signaturePath,omitempty"`
	PrivatePath   string `json:"privatePath,omitempty"`
	PublicPath    string `json:"publicPath,omitempty"`
}

func GenerateKeyPair(privatePath, publicPath string) (Report, error) {
	if privatePath == "" || publicPath == "" || privatePath == publicPath {
		return Report{}, fmt.Errorf("distinct private-key and public-key paths are required")
	}
	if _, err := os.Lstat(privatePath); err == nil || !os.IsNotExist(err) {
		return Report{}, fmt.Errorf("private key path already exists")
	}
	if _, err := os.Lstat(publicPath); err == nil || !os.IsNotExist(err) {
		return Report{}, fmt.Errorf("public key path already exists")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Report{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return Report{}, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return Report{}, err
	}
	if err := writeExclusive(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		return Report{}, err
	}
	if err := writeExclusive(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o644); err != nil {
		_ = os.Remove(privatePath)
		return Report{}, err
	}
	return Report{Valid: true, KeyID: publicKeyID(publicKey), PrivatePath: privatePath, PublicPath: publicPath}, nil
}

func SignFile(payloadPath, privateKeyPath, outputPath string) (Report, error) {
	if payloadPath == "" || privateKeyPath == "" || outputPath == "" {
		return Report{}, fmt.Errorf("file, private-key, and output paths are required")
	}
	privateKey, err := readPrivateKey(privateKeyPath)
	if err != nil {
		return Report{}, err
	}
	digest, err := fileDigest(payloadPath)
	if err != nil {
		return Report{}, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signature := ed25519.Sign(privateKey, []byte(Domain+digest))
	envelope := Envelope{APIVersion: APIVersion, Kind: Kind, Algorithm: Algorithm, KeyID: publicKeyID(publicKey), PayloadSHA256: digest, Signature: base64.StdEncoding.EncodeToString(signature)}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return Report{}, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return Report{}, err
	}
	if err := writeExclusive(outputPath, append(canonical, '\n'), 0o644); err != nil {
		return Report{}, err
	}
	return Report{Valid: true, KeyID: envelope.KeyID, PayloadSHA256: digest, SignaturePath: outputPath}, nil
}

func VerifyFile(payloadPath, signaturePath, publicKeyPath string) (Report, error) {
	if payloadPath == "" || signaturePath == "" || publicKeyPath == "" {
		return Report{}, fmt.Errorf("file, signature, and public-key paths are required")
	}
	publicKey, err := readPublicKey(publicKeyPath)
	if err != nil {
		return Report{}, err
	}
	digest, err := fileDigest(payloadPath)
	if err != nil {
		return Report{}, err
	}
	raw, err := readBounded(signaturePath, keyLimit)
	if err != nil {
		return Report{}, err
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Report{}, fmt.Errorf("decode signature envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Report{}, fmt.Errorf("signature envelope has trailing content")
	}
	keyID := publicKeyID(publicKey)
	if envelope.APIVersion != APIVersion || envelope.Kind != Kind || envelope.Algorithm != Algorithm || envelope.KeyID != keyID {
		return Report{}, fmt.Errorf("signature envelope does not match the supplied Ed25519 key")
	}
	if envelope.PayloadSHA256 != digest {
		return Report{}, fmt.Errorf("signed payload digest mismatch")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, []byte(Domain+digest), signature) {
		return Report{}, fmt.Errorf("Ed25519 signature verification failed")
	}
	return Report{Valid: true, KeyID: keyID, PayloadSHA256: digest, SignaturePath: signaturePath, PublicPath: publicKeyPath}, nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private key is not a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private key permissions must not allow group or other access")
	}
	raw, err := readBounded(path, keyLimit)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("private key must be one PKCS#8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	return key, nil
}

func readPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := readBounded(path, keyLimit)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("public key must be one PKIX PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not Ed25519")
	}
	return key, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxSignedPayloadBytes {
		return "", fmt.Errorf("payload must be a regular file no larger than %d bytes", MaxSignedPayloadBytes)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func publicKeyID(key ed25519.PublicKey) string {
	digest := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, fmt.Errorf("file must be regular, non-symlink, and no larger than %d bytes", limit)
	}
	return os.ReadFile(path)
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

package signing

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetachedSignatureRoundTripAndTamperDetection(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "signing-private.pem")
	publicPath := filepath.Join(root, "signing-public.pem")
	report, err := GenerateKeyPair(privatePath, publicPath)
	if err != nil || report.KeyID == "" {
		t.Fatalf("generate key pair: %+v %v", report, err)
	}
	payloadPath := filepath.Join(root, "journal.ndjson")
	if err := os.WriteFile(payloadPath, []byte("exact export bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signaturePath := filepath.Join(root, "journal.ndjson.sig.json")
	signed, err := SignFile(payloadPath, privatePath, signaturePath)
	if err != nil || signed.KeyID != report.KeyID {
		t.Fatalf("sign: %+v %v", signed, err)
	}
	verified, err := VerifyFile(payloadPath, signaturePath, publicPath)
	if err != nil || !verified.Valid || verified.PayloadSHA256 != signed.PayloadSHA256 {
		t.Fatalf("verify: %+v %v", verified, err)
	}
	if err := os.WriteFile(payloadPath, []byte("changed export bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFile(payloadPath, signaturePath, publicPath); err == nil {
		t.Fatal("tampered payload passed signature verification")
	}
}

func TestPrivateKeyPermissionsFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX key permissions")
	}
	root := t.TempDir()
	privatePath := filepath.Join(root, "private.pem")
	publicPath := filepath.Join(root, "public.pem")
	if _, err := GenerateKeyPair(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(root, "payload")
	if err := os.WriteFile(payloadPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SignFile(payloadPath, privatePath, filepath.Join(root, "signature")); err == nil {
		t.Fatal("world-readable private key was accepted")
	}
}

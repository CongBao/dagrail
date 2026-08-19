//go:build windows

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func platformEndpoint(directory string) (string, error) {
	// The ACL, not the human-readable name, is the security boundary. Binding
	// the name to the owner-local runtime directory also keeps test processes
	// that explicitly inject a package-level directory independent.
	digest := sha256.Sum256([]byte(directory))
	return `\\.\pipe\dagrail-controller-` + hex.EncodeToString(digest[:8]), nil
}

func currentUserSDDL() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("D:P(A;;GA;;;%s)", user.User.Sid.String()), nil
}

func listenEndpoint(endpoint string) (net.Listener, error) {
	sddl, err := currentUserSDDL()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(endpoint, &winio.PipeConfig{SecurityDescriptor: sddl, MessageMode: false, InputBufferSize: 64 * 1024, OutputBufferSize: 64 * 1024})
}

func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

func removeEndpoint(string) {}

var _ = time.Second

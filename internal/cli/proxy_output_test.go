package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type rejectingControllerWriter struct{ err error }

func (writer rejectingControllerWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestControllerProxyDoesNotReportSuccessWhenReceiptDeliveryFails(t *testing.T) {
	failure := errors.New("capture pipe closed")
	if err := writeControllerStreams(rejectingControllerWriter{err: failure}, &bytes.Buffer{}, []byte(`{"confirmed":true}`), nil); !errors.Is(err, failure) || !strings.Contains(err.Error(), "controller receipt") {
		t.Fatalf("receipt write failure was not preserved: %v", err)
	}
	if err := writeControllerStreams(&bytes.Buffer{}, rejectingControllerWriter{err: failure}, nil, []byte("diagnostic")); !errors.Is(err, failure) || !strings.Contains(err.Error(), "controller diagnostic") {
		t.Fatalf("diagnostic write failure was not preserved: %v", err)
	}
}

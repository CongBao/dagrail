package service_test

import (
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/service"
)

func TestDoctorFindsEveryFirstPartyHarnessProvider(t *testing.T) {
	t.Setenv("DAGRAIL_HOME", t.TempDir())
	// A package test must not execute or depend on the user's installed
	// DAGrail runtime. An absent PATH launcher is a documented warning and does
	// not make the project unhealthy.
	t.Setenv("PATH", t.TempDir())
	svc, err := service.Init(t.TempDir(), "doctor-test")
	if err != nil {
		t.Fatal(err)
	}
	report := svc.Doctor()
	for _, check := range report.Checks {
		if strings.HasPrefix(check.Name, "harness-provider:") && check.Status != "pass" {
			t.Fatalf("first-party harness provider was not registered: %+v", check)
		}
	}
	if !report.Healthy {
		t.Fatalf("doctor unexpectedly reported an unhealthy fresh project: %+v", report)
	}
}

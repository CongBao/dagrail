package service

import "testing"

func TestSupportBuildMetadataRejectsPathsAndControls(t *testing.T) {
	for _, value := range []string{"/private/build/path", `C:\\private\\build`, "line\nbreak", "", "https://example.com/build"} {
		if observed := safeSupportBuildValue(value); observed != "redacted" {
			t.Fatalf("unsafe build metadata was retained: %q", observed)
		}
	}
	for _, value := range []string{"0.13.0", "abcdef123", "2026-08-14T00:00:00Z", "unknown"} {
		if observed := safeSupportBuildValue(value); observed != value {
			t.Fatalf("safe build metadata was redacted: %q", value)
		}
	}
}

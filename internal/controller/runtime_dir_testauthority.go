//go:build dagrail_testauthority

package controller

import "os"

// taggedRuntimeDir exists only in explicitly tagged recovery/compatibility test
// binaries. Production binaries intentionally have no environment-selected
// controller socket namespace.
func taggedRuntimeDir() string { return os.Getenv("DAGRAIL_TEST_CONTROLLER_DIR") }

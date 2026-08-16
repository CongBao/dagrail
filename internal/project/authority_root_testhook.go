//go:build dagrail_testauthority

package project

import "os"

// authorityRootFromTestEnvironment exists only in binaries built explicitly
// for the historical compatibility test. Production builds ignore this input.
func authorityRootFromTestEnvironment() string {
	return os.Getenv("DAGRAIL_TEST_AUTHORITY_HOME")
}

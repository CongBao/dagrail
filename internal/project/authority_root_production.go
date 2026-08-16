//go:build !dagrail_testauthority

package project

func authorityRootFromTestEnvironment() string {
	return ""
}

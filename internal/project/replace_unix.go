//go:build !windows

package project

import "os"

func replaceFileAtomic(source, destination string) error {
	return os.Rename(source, destination)
}

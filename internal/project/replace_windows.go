//go:build windows

package project

func replaceFileAtomic(source, destination string) error {
	return movePathWriteThrough(source, destination, true)
}

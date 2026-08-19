//go:build !windows

package project

import (
	"errors"
	"os"
)

func syncDirectoryPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func createDirectoryEntry(path string, mode os.FileMode) error {
	return os.Mkdir(path, mode)
}

func publishPathExclusive(source, destination string) error {
	return os.Link(source, destination)
}

func publishPathAtomic(source, destination string) error {
	return os.Rename(source, destination)
}

func publishDirectoryExclusive(source, destination string) error {
	return os.Rename(source, destination)
}

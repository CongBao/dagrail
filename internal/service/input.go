package service

import (
	"fmt"
	"io"
	"os"
)

const maxDefinitionBytes = 8 * 1024 * 1024

func readDefinitionFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("definition input must be a regular file")
	}
	if info.Size() > maxDefinitionBytes {
		return nil, fmt.Errorf("definition input exceeds %d bytes", maxDefinitionBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDefinitionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDefinitionBytes {
		return nil, fmt.Errorf("definition input exceeds %d bytes", maxDefinitionBytes)
	}
	return raw, nil
}

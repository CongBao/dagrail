package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/CongBao/dagrail/internal/domain"
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

func decodeStrictAuthorityJSON(raw []byte, target any) error {
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON input has trailing content")
	}
	return nil
}

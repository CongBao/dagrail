//go:build windows

package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func syncDirectoryPath(path string) error {
	path, err := extendedWindowsPath(path)
	if err != nil {
		return err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pointer,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	if !validDirectoryAttributes(information.FileAttributes) {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("durability handle is not a non-reparse directory")
	}
	return windows.CloseHandle(handle)
}

func validDirectoryAttributes(attributes uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

func createDirectoryEntry(path string, mode os.FileMode) error {
	parent := filepath.Dir(path)
	temporary, err := os.MkdirTemp(parent, ".dagrail-directory-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	if err := movePathWriteThrough(temporary, path, false); err != nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			return fmt.Errorf("%w: %s", os.ErrExist, path)
		}
		return err
	}
	return nil
}

func publishPathExclusive(source, destination string) error {
	return movePathWriteThrough(source, destination, false)
}

func publishPathAtomic(source, destination string) error {
	return movePathWriteThrough(source, destination, false)
}

func publishDirectoryExclusive(source, destination string) error {
	return movePathWriteThrough(source, destination, false)
}

func movePathWriteThrough(source, destination string, replace bool) error {
	source, err := extendedWindowsPath(source)
	if err != nil {
		return err
	}
	destination, err = extendedWindowsPath(destination)
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}

func extendedWindowsPath(path string) (string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(normalized, `\\?\`) || strings.HasPrefix(normalized, `\??\`) || strings.HasPrefix(normalized, `\\.\`) {
		return normalized, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = strings.ReplaceAll(filepath.Clean(absolute), "/", `\`)
	if len(absolute) < 248 {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`), nil
	}
	return `\\?\` + absolute, nil
}

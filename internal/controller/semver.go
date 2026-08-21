package controller

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	core       [3]uint64
	prerelease []string
}

func compareSemanticVersions(left, right string) (int, bool) {
	a, valid := parseSemanticVersion(left)
	if !valid {
		return 0, false
	}
	b, valid := parseSemanticVersion(right)
	if !valid {
		return 0, false
	}
	for index := range a.core {
		if a.core[index] < b.core[index] {
			return -1, true
		}
		if a.core[index] > b.core[index] {
			return 1, true
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, true
	}
	if len(a.prerelease) == 0 {
		return 1, true
	}
	if len(b.prerelease) == 0 {
		return -1, true
	}
	for index := 0; index < len(a.prerelease) && index < len(b.prerelease); index++ {
		comparison := comparePrereleaseIdentifier(a.prerelease[index], b.prerelease[index])
		if comparison != 0 {
			return comparison, true
		}
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1, true
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1, true
	}
	return 0, true
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	var parsed semanticVersion
	if strings.HasPrefix(value, "v") {
		value = strings.TrimPrefix(value, "v")
	}
	if build := strings.IndexByte(value, '+'); build >= 0 {
		if !validIdentifiers(value[build+1:], false) {
			return parsed, false
		}
		value = value[:build]
	}
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		if !validIdentifiers(value[dash+1:], true) {
			return parsed, false
		}
		parsed.prerelease = strings.Split(value[dash+1:], ".")
		value = value[:dash]
	}
	core := strings.Split(value, ".")
	if len(core) != 3 {
		return parsed, false
	}
	for index, identifier := range core {
		if identifier == "" || (len(identifier) > 1 && identifier[0] == '0') {
			return parsed, false
		}
		number, err := strconv.ParseUint(identifier, 10, 64)
		if err != nil {
			return parsed, false
		}
		parsed.core[index] = number
	}
	return parsed, true
}

func validIdentifiers(value string, prerelease bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" || (prerelease && numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0') {
			return false
		}
		for _, character := range identifier {
			if !(character >= '0' && character <= '9') && !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && character != '-' {
				return false
			}
		}
	}
	return true
}

func numericIdentifier(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func comparePrereleaseIdentifier(left, right string) int {
	leftNumeric, rightNumeric := numericIdentifier(left), numericIdentifier(right)
	if leftNumeric && rightNumeric {
		if len(left) < len(right) {
			return -1
		}
		if len(left) > len(right) {
			return 1
		}
		return strings.Compare(left, right)
	}
	if leftNumeric {
		return -1
	}
	if rightNumeric {
		return 1
	}
	return strings.Compare(left, right)
}

package component

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []versionIdentifier
}

type versionIdentifier struct {
	text    string
	numeric bool
	value   uint64
}

func parseSemanticVersion(raw string) (semanticVersion, error) {
	var parsed semanticVersion
	if raw == "" || strings.HasPrefix(raw, "v") {
		return parsed, fmt.Errorf("%q is not SemVer 2.0.0", raw)
	}
	coreAndPre := raw
	if buildAt := strings.IndexByte(coreAndPre, '+'); buildAt >= 0 {
		build := coreAndPre[buildAt+1:]
		if err := validateVersionIdentifiers(build, false); err != nil {
			return parsed, fmt.Errorf("invalid build metadata: %w", err)
		}
		coreAndPre = coreAndPre[:buildAt]
	}
	core := coreAndPre
	if preAt := strings.IndexByte(coreAndPre, '-'); preAt >= 0 {
		pre := coreAndPre[preAt+1:]
		if err := validateVersionIdentifiers(pre, true); err != nil {
			return parsed, fmt.Errorf("invalid prerelease: %w", err)
		}
		for _, item := range strings.Split(pre, ".") {
			identifier := versionIdentifier{text: item}
			if isNumericIdentifier(item) {
				identifier.numeric = true
				identifier.value, _ = strconv.ParseUint(item, 10, 64)
			}
			parsed.prerelease = append(parsed.prerelease, identifier)
		}
		core = coreAndPre[:preAt]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("%q must contain major.minor.patch", raw)
	}
	values := []*uint64{&parsed.major, &parsed.minor, &parsed.patch}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') || !isNumericIdentifier(part) {
			return semanticVersion{}, fmt.Errorf("invalid numeric version component %q", part)
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("invalid numeric version component %q: %w", part, err)
		}
		*values[i] = value
	}
	return parsed, nil
}

func validateVersionIdentifiers(raw string, prerelease bool) error {
	if raw == "" {
		return fmt.Errorf("identifier list is empty")
	}
	for _, item := range strings.Split(raw, ".") {
		if item == "" {
			return fmt.Errorf("empty identifier")
		}
		for i := 0; i < len(item); i++ {
			c := item[i]
			if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && c != '-' {
				return fmt.Errorf("invalid character in identifier %q", item)
			}
		}
		if prerelease && isNumericIdentifier(item) && len(item) > 1 && item[0] == '0' {
			return fmt.Errorf("numeric prerelease identifier %q has a leading zero", item)
		}
		if prerelease && isNumericIdentifier(item) {
			if _, err := strconv.ParseUint(item, 10, 64); err != nil {
				return fmt.Errorf("numeric prerelease identifier %q overflows", item)
			}
		}
	}
	return nil
}

func isNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func compareSemanticVersions(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	limit := len(left.prerelease)
	if len(right.prerelease) < limit {
		limit = len(right.prerelease)
	}
	for i := 0; i < limit; i++ {
		a, b := left.prerelease[i], right.prerelease[i]
		switch {
		case a.numeric && b.numeric:
			if a.value < b.value {
				return -1
			}
			if a.value > b.value {
				return 1
			}
		case a.numeric:
			return -1
		case b.numeric:
			return 1
		default:
			if a.text < b.text {
				return -1
			}
			if a.text > b.text {
				return 1
			}
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

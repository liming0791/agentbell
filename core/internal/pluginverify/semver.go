package pluginverify

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	major uint64
	minor uint64
	patch uint64
	pre   []string
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return semanticVersion{}, false
	}
	coreAndBuild := strings.SplitN(value, "+", 2)
	if strings.Count(value, "+") > 1 ||
		(len(coreAndBuild) == 2 && !validIdentifiers(coreAndBuild[1], false)) {
		return semanticVersion{}, false
	}
	coreAndPre := strings.SplitN(coreAndBuild[0], "-", 2)
	if (len(coreAndPre) == 2 &&
		coreAndPre[1] == "") ||
		(len(coreAndPre) == 2 && !validIdentifiers(coreAndPre[1], true)) {
		return semanticVersion{}, false
	}
	numbers := strings.Split(coreAndPre[0], ".")
	if len(numbers) != 3 {
		return semanticVersion{}, false
	}
	parsed := make([]uint64, 3)
	for index, number := range numbers {
		if !validNumericIdentifier(number) {
			return semanticVersion{}, false
		}
		current, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			return semanticVersion{}, false
		}
		parsed[index] = current
	}
	result := semanticVersion{major: parsed[0], minor: parsed[1], patch: parsed[2]}
	if len(coreAndPre) == 2 {
		result.pre = strings.Split(coreAndPre[1], ".")
	}
	return result, true
}

func validIdentifiers(value string, enforceNumericLeadingZero bool) bool {
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if part == "" {
			return false
		}
		numeric := true
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') ||
				character == '-') {
				return false
			}
			if character < '0' || character > '9' {
				numeric = false
			}
		}
		if enforceNumericLeadingZero && numeric && !validNumericIdentifier(part) {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareSemanticVersions(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{
		{left.major, right.major},
		{left.minor, right.minor},
		{left.patch, right.patch},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.pre) == 0 && len(right.pre) == 0 {
		return 0
	}
	if len(left.pre) == 0 {
		return 1
	}
	if len(right.pre) == 0 {
		return -1
	}
	for index := 0; index < len(left.pre) && index < len(right.pre); index++ {
		leftNumeric := numericIdentifier(left.pre[index])
		rightNumeric := numericIdentifier(right.pre[index])
		switch {
		case leftNumeric && rightNumeric:
			if len(left.pre[index]) < len(right.pre[index]) {
				return -1
			}
			if len(left.pre[index]) > len(right.pre[index]) {
				return 1
			}
			if left.pre[index] < right.pre[index] {
				return -1
			}
			if left.pre[index] > right.pre[index] {
				return 1
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		default:
			if left.pre[index] < right.pre[index] {
				return -1
			}
			if left.pre[index] > right.pre[index] {
				return 1
			}
		}
	}
	if len(left.pre) < len(right.pre) {
		return -1
	}
	if len(left.pre) > len(right.pre) {
		return 1
	}
	return 0
}

func numericIdentifier(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

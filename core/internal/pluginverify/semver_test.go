package pluginverify

import "testing"

func TestParseSemanticVersion(t *testing.T) {
	for _, value := range []string{
		"0.0.0",
		"2.1.0",
		"2.1.0-rc.1",
		"2.1.0-alpha-beta.9+build.42",
		"18446744073709551615.0.0",
	} {
		if _, ok := parseSemanticVersion(value); !ok {
			t.Fatalf("valid version rejected: %q", value)
		}
	}
	for _, value := range []string{
		"",
		" 2.1.0",
		"v2.1.0",
		"2.1",
		"2.1.0.1",
		"02.1.0",
		"2.01.0",
		"2.1.00",
		"18446744073709551616.0.0",
		"2.1.0-",
		"2.1.0-01",
		"2.1.0-alpha..one",
		"2.1.0-alpha!",
		"2.1.0+",
		"2.1.0+build+other",
	} {
		if _, ok := parseSemanticVersion(value); ok {
			t.Fatalf("invalid version accepted: %q", value)
		}
	}
}

func TestSemanticVersionPrecedence(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	for index := 0; index < len(ordered)-1; index++ {
		left, leftOK := parseSemanticVersion(ordered[index])
		right, rightOK := parseSemanticVersion(ordered[index+1])
		if !leftOK || !rightOK || compareSemanticVersions(left, right) >= 0 {
			t.Fatalf("%q must precede %q", ordered[index], ordered[index+1])
		}
		if compareSemanticVersions(right, left) <= 0 {
			t.Fatalf("%q must follow %q", ordered[index+1], ordered[index])
		}
	}
	release, _ := parseSemanticVersion("1.0.0+build.1")
	sameRelease, _ := parseSemanticVersion("1.0.0+build.2")
	if compareSemanticVersions(release, sameRelease) != 0 {
		t.Fatal("build metadata changed precedence")
	}
}

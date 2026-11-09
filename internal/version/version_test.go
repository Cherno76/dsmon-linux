package version

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestCurrentVersionUsesThreeBoundedComponents(t *testing.T) {
	match := regexp.MustCompile(`^([0-9]{1,2})\.([0-9]{1,2})\.([0-9]{1,2})$`).FindStringSubmatch(Current)
	if match == nil {
		t.Fatalf("version %q is not major.minor.patch", Current)
	}
	for index, part := range match[1:] {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 99 {
			t.Fatalf("version component %d = %q, want 0–99", index, part)
		}
	}
}

func TestCurrentVersionHasNoWhitespace(t *testing.T) {
	if strings.TrimSpace(Current) != Current {
		t.Fatalf("version contains surrounding whitespace: %q", Current)
	}
}

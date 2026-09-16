package version

import "testing"

func TestStringPrefersLdflags(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"
	if String() != "v1.2.3" {
		t.Errorf("String() = %q", String())
	}
	Version = ""
	if String() == "" {
		t.Error("String() is empty without ldflags")
	}
}

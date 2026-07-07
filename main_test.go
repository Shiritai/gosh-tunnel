package main

import "testing"

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		arg  string
		want targetKind
	}{
		{"1234", targetLocalPort},
		{"80", targetLocalPort},
		// Numeric but out of port range: still a port so the user gets a
		// clear "invalid port" error instead of a server lookup failure.
		{"0", targetLocalPort},
		{"70000", targetLocalPort},
		{"gpu-server", targetServer},
		{"gpu-box-1", targetServer},
		{"gpu-box-1-8080:80", targetName},
		{"gpu-server-1234:80", targetName},
	}
	for _, c := range cases {
		if got := classifyTarget(c.arg); got != c.want {
			t.Errorf("classifyTarget(%q) = %v, want %v", c.arg, got, c.want)
		}
	}
}

func TestValidLocalPort(t *testing.T) {
	valid := []string{"1", "1234", "65535"}
	invalid := []string{"0", "-5", "70000", "65536"}
	for _, s := range valid {
		if _, err := parseLocalPort(s); err != nil {
			t.Errorf("parseLocalPort(%q) unexpected error: %v", s, err)
		}
	}
	for _, s := range invalid {
		if _, err := parseLocalPort(s); err == nil {
			t.Errorf("parseLocalPort(%q) expected error, got nil", s)
		}
	}
}

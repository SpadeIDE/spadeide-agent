package discovery

import "testing"

func TestSanitizeHost(t *testing.T) {
	cases := map[string]string{
		"":              "spade-agent", // may become hostname — only check non-empty path below
		"my-host":       "my-host",
		"My Host 01":    "My Host 01",
		"bad/name*here": "bad-name-here",
		"  spaced  ":    "spaced",
	}
	for in, want := range cases {
		if in == "" {
			got := sanitizeHost("")
			if got == "" {
				t.Fatalf("sanitizeHost(\"\") empty")
			}
			continue
		}
		if got := sanitizeHost(in); got != want {
			t.Fatalf("sanitizeHost(%q)=%q want %q", in, got, want)
		}
	}
}

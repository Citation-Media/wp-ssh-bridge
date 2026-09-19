package app

import (
	"strings"
	"testing"
)

func TestParseDestinationAcceptsEverySSHForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want sshDestination
	}{
		{"prod", sshDestination{Host: "prod"}},
		{"deploy@production.example.com", sshDestination{User: "deploy", Host: "production.example.com"}},
		{"deploy@production.example.com:2222", sshDestination{User: "deploy", Host: "production.example.com", Port: "2222"}},
		{"production.example.com:2222", sshDestination{Host: "production.example.com", Port: "2222"}},
		{"ssh://deploy@production.example.com:2222", sshDestination{User: "deploy", Host: "production.example.com", Port: "2222"}},
		{"ssh://production.example.com", sshDestination{Host: "production.example.com"}},
		{"[2001:db8::1]", sshDestination{Host: "[2001:db8::1]"}},
		{"deploy@[2001:db8::1]:2222", sshDestination{User: "deploy", Host: "[2001:db8::1]", Port: "2222"}},
		{"ssh://[2001:db8::1]:22", sshDestination{Host: "[2001:db8::1]", Port: "22"}},
		{"  deploy@prod  ", sshDestination{User: "deploy", Host: "prod"}},
	}
	for _, tc := range cases {
		got, err := parseDestination(tc.raw)
		if err != nil {
			t.Errorf("parseDestination(%q) error = %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDestination(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestParseDestinationRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                            "empty",
		"deploy@":                     "missing a host",
		"deploy@prod extra":           "whitespace",
		"prod/var/www":                "path",
		"2001:db8::1":                 "brackets",
		"[2001:db8::1":                "unclosed",
		"[2001:db8::1]x":              "unexpected text",
		"deploy@prod:abc":             "numeric",
		"dep;loy@prod":                "user in destination",
		"deploy@pr$(id)od":            "host in destination",
		"deploy@prod:22:33":           "brackets",
		"ssh://deploy@prod:22/public": "path",
	}
	for raw, fragment := range cases {
		_, err := parseDestination(raw)
		if err == nil {
			t.Errorf("parseDestination(%q) accepted an unsafe value", raw)
			continue
		}
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("parseDestination(%q) error %q does not mention %q", raw, err, fragment)
		}
	}
}

func TestSSHDestinationAddressAndDescribe(t *testing.T) {
	t.Parallel()
	if got := (sshDestination{Host: "prod"}).address(); got != "prod" {
		t.Errorf("address() without user = %q", got)
	}
	if got := (sshDestination{User: "deploy", Host: "prod", Port: "22"}).describe(); got != "deploy@prod" {
		t.Errorf("describe() should omit the default port, got %q", got)
	}
	if got := (sshDestination{User: "deploy", Host: "prod", Port: "2222"}).describe(); got != "deploy@prod:2222" {
		t.Errorf("describe() = %q", got)
	}
}

func TestParseSSHConfigDumpReadsResolvedValues(t *testing.T) {
	t.Parallel()
	output := "user deploy\nhostname production.example.com\nport 2222\nidentityagent /tmp/agent.sock\n"
	got, ok := parseSSHConfigDump(output)
	if !ok {
		t.Fatal("parseSSHConfigDump() reported no host")
	}
	want := sshDestination{User: "deploy", Host: "production.example.com", Port: "2222"}
	if got != want {
		t.Fatalf("parseSSHConfigDump() = %+v, want %+v", got, want)
	}
	if _, ok := parseSSHConfigDump("nothing useful\n"); ok {
		t.Fatal("parseSSHConfigDump() accepted output without a hostname")
	}
}

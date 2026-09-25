package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// sshDestination is the parsed form of a pull_destination or push_destination value.
type sshDestination struct {
	User string
	Host string
	Port string
}

// parseDestination accepts the address forms ssh itself accepts — an alias from
// ~/.ssh/config, [user@]host, and ssh://[user@]host[:port] — plus the common
// [user@]host:port shorthand.
//
// IPv6 literals are refused rather than bracketed: macOS' default rsync splits a
// remote spec at its first colon and cannot pass one on, so the only form that works
// everywhere is an alias whose HostName is the address.
func parseDestination(raw string) (sshDestination, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return sshDestination{}, errors.New("destination is empty")
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return sshDestination{}, fmt.Errorf("destination %q must not contain whitespace", raw)
	}
	value = strings.TrimPrefix(value, "ssh://")
	if strings.Contains(value, "/") {
		return sshDestination{}, fmt.Errorf("destination %q must not contain a path; the remote WordPress path is configured separately", raw)
	}

	dest := sshDestination{}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		dest.User = value[:at]
		value = value[at+1:]
		if !validSSHPart.MatchString(dest.User) {
			return sshDestination{}, fmt.Errorf("user in destination %q must contain only letters, numbers, dots, underscores, or hyphens", raw)
		}
	}

	if strings.Contains(value, "[") || strings.Count(value, ":") > 1 {
		return sshDestination{}, fmt.Errorf("destination %q is an IPv6 address; add a Host block with that HostName to ~/.ssh/config and use its alias as the destination", raw)
	}
	dest.Host, dest.Port, _ = strings.Cut(value, ":")
	if dest.Host == "" {
		return sshDestination{}, fmt.Errorf("destination %q is missing a host", raw)
	}
	if !validSSHPart.MatchString(dest.Host) {
		return sshDestination{}, fmt.Errorf("host in destination %q must contain only letters, numbers, dots, underscores, or hyphens", raw)
	}
	if dest.Port != "" {
		if _, err := strconv.Atoi(dest.Port); err != nil {
			return sshDestination{}, fmt.Errorf("port in destination %q must be numeric", raw)
		}
	}
	return dest, nil
}

// address returns the [user@]host form shared by ssh and rsync.
func (d sshDestination) address() string {
	if d.User != "" {
		return d.User + "@" + d.Host
	}
	return d.Host
}

// describe renders a resolved destination for messages, omitting the default port.
// ssh -G may resolve an alias to an IPv6 HostName, which is bracketed for display.
func (d sshDestination) describe() string {
	out := d.address()
	if strings.Contains(d.Host, ":") {
		out = strings.Replace(out, d.Host, "["+d.Host+"]", 1)
	}
	if d.Port != "" && d.Port != "22" {
		out += ":" + d.Port
	}
	return out
}

// resolveSSHDestination asks ssh -G what an alias or URL resolves to, without connecting.
// ok is false when ssh is unavailable or printed nothing usable, so callers can stay quiet.
func resolveSSHDestination(ctx context.Context, target RemoteTarget) (sshDestination, bool) {
	args := plainSSHArgv(target, "-G", sshTarget(target))
	output, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
	if err != nil {
		return sshDestination{}, false
	}
	return parseSSHConfigDump(string(output))
}

// parseSSHConfigDump reads the "key value" lines of ssh -G output.
func parseSSHConfigDump(output string) (sshDestination, bool) {
	dest := sshDestination{}
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		switch strings.ToLower(key) {
		case "user":
			dest.User = value
		case "hostname":
			dest.Host = value
		case "port":
			dest.Port = value
		}
	}
	return dest, dest.Host != ""
}

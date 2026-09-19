package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// sshDestination is the parsed form of a pull_destination or push_destination value.
type sshDestination struct {
	User string
	Host string
	Port string
}

var (
	validDestinationHost = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	validIPv6Literal     = regexp.MustCompile(`^\[[0-9A-Fa-f:.%]+\]$`)
)

// parseDestination accepts the address forms ssh itself accepts — an alias from
// ~/.ssh/config, [user@]host, and ssh://[user@]host[:port] — plus the common
// [user@]host:port shorthand. IPv6 addresses must be bracketed so the port is unambiguous.
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

	// A bracketed IPv6 literal keeps its own colons; otherwise a single trailing :port is split off.
	hostPart := value
	switch {
	case strings.HasPrefix(value, "["):
		end := strings.Index(value, "]")
		if end < 0 {
			return sshDestination{}, fmt.Errorf("destination %q has an unclosed IPv6 bracket", raw)
		}
		hostPart = value[:end+1]
		if rest := value[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return sshDestination{}, fmt.Errorf("destination %q has unexpected text after the IPv6 address", raw)
			}
			dest.Port = rest[1:]
		}
	case strings.Count(value, ":") > 1:
		return sshDestination{}, fmt.Errorf("destination %q looks like an IPv6 address; wrap it in brackets, for example [2001:db8::1]:22", raw)
	case strings.Contains(value, ":"):
		hostPart, dest.Port, _ = strings.Cut(value, ":")
	}

	dest.Host = hostPart
	if dest.Host == "" {
		return sshDestination{}, fmt.Errorf("destination %q is missing a host", raw)
	}
	if !validDestinationHost.MatchString(dest.Host) && !validIPv6Literal.MatchString(dest.Host) {
		return sshDestination{}, fmt.Errorf("host in destination %q must contain only letters, numbers, dots, underscores, or hyphens, or be a bracketed IPv6 address", raw)
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
func (d sshDestination) describe() string {
	out := d.address()
	if d.Port != "" && d.Port != "22" {
		out += ":" + d.Port
	}
	return out
}

// resolveSSHDestination asks ssh -G what an alias or URL resolves to, without connecting.
// ok is false when ssh is unavailable or printed nothing usable, so callers can stay quiet.
func resolveSSHDestination(ctx context.Context, target RemoteTarget) (sshDestination, bool) {
	args := sshArgv(target, "-G", sshTarget(target))
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

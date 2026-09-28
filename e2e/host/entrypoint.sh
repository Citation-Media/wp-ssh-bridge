#!/bin/sh
# Prepares one simulated machine and runs sshd in the foreground.
#
# Keys come from /run/wpsb-e2e, generated once by `wpsb-e2e up` on the host:
# every machine shares one host key (known as wpsb-e2e) and trusts one login
# key, which is also installed as the machine's own client key so a clone on
# the target can reach the source like a forwarded agent would.
#
# The WPSB_* variables turn a machine into the hosts the CLI has workarounds
# for; each one is documented next to its handling below.
set -eu

KEYS=/run/wpsb-e2e

install -m 600 "$KEYS/ssh_host_ed25519_key" /etc/ssh/ssh_host_ed25519_key
install -m 644 "$KEYS/ssh_host_ed25519_key.pub" /etc/ssh/ssh_host_ed25519_key.pub
printf 'wpsb-e2e %s\n' "$(cut -d' ' -f1,2 "$KEYS/ssh_host_ed25519_key.pub")" > /etc/ssh/ssh_known_hosts

install -d -o site -g site -m 700 /home/site/.ssh
install -o site -g site -m 600 "$KEYS/id_ed25519.pub" /home/site/.ssh/authorized_keys
install -o site -g site -m 600 "$KEYS/id_ed25519" /home/site/.ssh/id_ed25519

# The CLI under test, built for Linux by `wpsb-e2e build`. A symlink, so a
# rebuild on the host takes effect without restarting the container.
ln -sf /opt/wpsb-e2e/bin/wp-ssh-bridge /usr/local/bin/wp-ssh-bridge

# No rsync on the host, as on many shared hosts: the CLI falls back to its
# scp/tar transport.
if [ "${WPSB_NO_RSYNC:-}" = 1 ]; then
    rm -f /usr/bin/rsync
fi

# Hostinger-style hardening: a php.ini that disables the functions WP-CLI's
# `wp db export` needs. The CLI re-enables them for the export only, through
# `php -c <ini> -d disable_functions=…`.
if [ -n "${WPSB_DISABLE_FUNCTIONS:-}" ]; then
    cp /usr/local/etc/php/php.ini-production /usr/local/etc/php/php.ini
    printf '\ndisable_functions = %s\n' "$WPSB_DISABLE_FUNCTIONS" >> /usr/local/etc/php/php.ini
fi

# Netcup-style MariaDB client: `mysql` and `mysqldump` report MariaDB, but the
# `mariadb` and `mariadb-dump` names are missing. Debian links the old names
# to the new ones, so the binaries are copied over their links first.
if [ "${WPSB_MARIADB_LEGACY_NAMES:-}" = 1 ]; then
    for name in mysql mysqldump; do
        real=$(readlink -f "/usr/bin/$name")
        if [ "$real" != "/usr/bin/$name" ]; then
            cp --remove-destination "$real" "/usr/bin/$name"
        fi
    done
    rm -f /usr/bin/mariadb /usr/bin/mariadb-dump
fi

exec /usr/sbin/sshd -D -e

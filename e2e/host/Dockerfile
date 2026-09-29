# One image for every simulated machine: the pull source, its legacy twin,
# the push and clone target, and the standalone workstation. Debian rather
# than Alpine, because shared hosts run a GNU userland and the CLI's remote
# scripts should meet one.
FROM php:8.3-cli-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        openssh-server openssh-client rsync mariadb-client gzip tar less \
    && rm -rf /var/lib/apt/lists/* \
    && docker-php-ext-install mysqli \
    && printf 'memory_limit = 512M\n' > /usr/local/etc/php/conf.d/wpsb-e2e.ini

ARG WP_CLI_VERSION=2.12.0
RUN curl -fsSL -o /usr/local/bin/wp \
        "https://github.com/wp-cli/wp-cli/releases/download/v${WP_CLI_VERSION}/wp-cli-${WP_CLI_VERSION}.phar" \
    && chmod +x /usr/local/bin/wp

# WordPress core is baked in so seeding works offline and in seconds.
ARG WP_VERSION=latest
RUN wp core download --allow-root --path=/usr/src/wordpress --version="${WP_VERSION}"

# The SSH login. Hosts rarely hand out root, and the CLI must not depend on it.
RUN useradd --create-home --uid 1000 --shell /bin/bash site \
    && install -d -o site -g site /var/www/site /work \
    && mkdir -p /run/sshd \
    && rm -f /etc/ssh/ssh_host_*

COPY sshd_config /etc/ssh/sshd_config.d/wpsb-e2e.conf
COPY ssh_config /etc/ssh/ssh_config.d/wpsb-e2e.conf
COPY entrypoint.sh /usr/local/bin/wpsb-entrypoint

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/wpsb-entrypoint"]

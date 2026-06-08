#!/usr/bin/env bats

setup() {
  set -eu -o pipefail
  export ADDON_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  export TEST_PROJECT="${TEST_PROJECT:-wp-ssh-addon-test}"
  export TEST_DIR="${BATS_TEST_TMPDIR}/${TEST_PROJECT}"
}

@test "add-on files have valid syntax" {
  bash -n "${ADDON_DIR}/pull/wp-ssh-pull.sh"
  bash -n "${ADDON_DIR}/commands/web/wp-plugins-removal"
  ruby -e 'require "yaml"; ARGV.each { |path| YAML.load_file(path) }' \
    "${ADDON_DIR}/install.yaml" \
    "${ADDON_DIR}/config.wp-ssh.yaml" \
    "${ADDON_DIR}/providers/wp-ssh.yaml"
}

@test "install from local directory into a WordPress project" {
  mkdir -p "${TEST_DIR}"
  cd "${TEST_DIR}"

  ddev config --project-type=wordpress --docroot="" --project-name="${TEST_PROJECT}" --database=mariadb:11.8
  ddev add-on get "${ADDON_DIR}"

  test -f .ddev/providers/wp-ssh.yaml
  test -x .ddev/pull/wp-ssh-pull.sh
  test -x .ddev/commands/web/wp-plugins-removal
  test -f .ddev/commands/wp-plugins-removal.txt
  test -f .ddev/config.wp-ssh.yaml

  ddev debug configyaml >/dev/null
}

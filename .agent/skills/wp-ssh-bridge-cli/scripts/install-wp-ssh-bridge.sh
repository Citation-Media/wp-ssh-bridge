#!/usr/bin/env bash
set -eu -o pipefail

repo_url="https://github.com/Citation-Media/wp-ssh-bridge"
repo="${repo_url#https://github.com/}"
target_dir="${1:-.}"
version="${2:-${WP_SSH_BRIDGE_VERSION:-}}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

normalize_os() {
  case "$(uname -s)" in
    Darwin) printf 'darwin' ;;
    Linux) printf 'linux' ;;
    *) die "Unsupported OS: $(uname -s)" ;;
  esac
}

normalize_arch() {
  case "$(uname -m)" in
    arm64|aarch64) printf 'arm64' ;;
    x86_64|amd64) printf 'amd64' ;;
    *) die "Unsupported architecture: $(uname -m)" ;;
  esac
}

if [ -n "${version}" ] && [ "${version#v}" = "${version}" ]; then
  version="v${version}"
fi

target_dir="${target_dir%/}"
ddev_dir="${target_dir}/.ddev"
[ -f "${ddev_dir}/config.yaml" ] || die "Run from a DDEV project folder or pass one as the first argument."

command -v gh >/dev/null || die "Install gh and authenticate with access to ${repo_url}."
command -v tar >/dev/null || die "tar is required."

artifact_os="$(normalize_os)"
artifact_arch="$(normalize_arch)"
artifact_pattern="wp-ssh-bridge_*_${artifact_os}_${artifact_arch}.tar.gz"
if [ -n "${version}" ]; then
  artifact_pattern="wp-ssh-bridge_${version}_${artifact_os}_${artifact_arch}.tar.gz"
fi

bin_dir="${ddev_dir}/bin"

mkdir -p "${bin_dir}"
rm -f "${bin_dir}"/wp-ssh-bridge_*_"${artifact_os}"_"${artifact_arch}".tar.gz

gh_args=(release download)
if [ -n "${version}" ]; then
  gh_args+=("${version}")
fi

gh "${gh_args[@]}" \
  --repo "${repo}" \
  --pattern "${artifact_pattern}" \
  --dir "${bin_dir}" \
  --clobber

set -- "${bin_dir}"/wp-ssh-bridge_*_"${artifact_os}"_"${artifact_arch}".tar.gz
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  die "Expected one artifact matching ${artifact_pattern}."
fi

archive="$1"

tar -C "${bin_dir}" -xzf "${archive}"
rm "${archive}"

chmod +x "${bin_dir}/wp-ssh-bridge"

printf 'Installed %s\n' "${bin_dir}/wp-ssh-bridge"

#!/usr/bin/env bash
set -eu -o pipefail

repo_url="https://github.com/Citation-Media/wp-ssh-bridge"
repo="${repo_url#https://github.com/}"

if [ "$#" -gt 1 ]; then
  printf 'Usage: %s [target-dir]\n' "$0" >&2
  exit 1
fi

target_dir="${1:-.}"

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

target_dir="${target_dir%/}"
[ -n "${target_dir}" ] || target_dir="."

command -v gh >/dev/null || die "Install gh and authenticate with access to ${repo_url}."
command -v tar >/dev/null || die "tar is required."

artifact_os="$(normalize_os)"
artifact_arch="$(normalize_arch)"
artifact_pattern="wp-ssh-bridge_*_${artifact_os}_${artifact_arch}.tar.gz"

if [ -f "${target_dir}/.ddev/config.yaml" ]; then
  bin_dir="${target_dir}/.ddev/bin"
else
  bin_dir="${target_dir}"
fi

mkdir -p "${bin_dir}"
rm -f "${bin_dir}"/wp-ssh-bridge_*_"${artifact_os}"_"${artifact_arch}".tar.gz

gh release download \
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

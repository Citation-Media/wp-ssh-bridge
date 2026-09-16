// Resolves, downloads, and verifies the wp-ssh-bridge binary for this platform.
//
// The npm package carries no binary of its own. It fetches the archive from the
// GitHub release whose tag matches this package's version, so the version in
// package-lock.json is the version the project runs. Nothing here needs a
// dependency: fetch, node:crypto, and the system tar cover it.
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

const OS_BY_PLATFORM = { darwin: "darwin", linux: "linux" };
const ARCH_BY_CPU = { x64: "amd64", arm64: "arm64" };

export const binaryPath = join(packageRoot, "vendor", "wp-ssh-bridge");

export function readVersion() {
  return JSON.parse(readFileSync(join(packageRoot, "package.json"), "utf8")).version;
}

// A build of the CLI already on disk wins over anything downloaded. This is how
// you point the package at a locally compiled binary while developing.
export function overriddenBinary() {
  const override = process.env.WP_SSH_BRIDGE_BINARY;
  if (!override) return null;
  if (!existsSync(override)) {
    throw new Error(`WP_SSH_BRIDGE_BINARY points at ${override}, which does not exist`);
  }
  return override;
}

export function target() {
  const os = OS_BY_PLATFORM[process.platform];
  const arch = ARCH_BY_CPU[process.arch];
  if (!os || !arch) {
    throw new Error(
      `wp-ssh-bridge has no build for ${process.platform}/${process.arch}. ` +
        "Supported: macOS and Linux on x64 and arm64.",
    );
  }
  return { os, arch };
}

async function download(url) {
  const response = await fetch(url, { redirect: "follow" });
  if (!response.ok) {
    throw new Error(`download failed with HTTP ${response.status}: ${url}`);
  }
  return Buffer.from(await response.arrayBuffer());
}

function expectedDigest(checksums, archiveName) {
  for (const line of checksums.split("\n")) {
    const [digest, name] = line.trim().split(/\s+/);
    if (name === archiveName) return digest;
  }
  throw new Error(`no checksum published for ${archiveName}`);
}

// Downloads, verifies, and unpacks into vendor/. The archive is extracted in a
// temporary directory and moved into place, so a failed or concurrent install
// never leaves a half-written binary behind.
export async function install({ log = () => {} } = {}) {
  const version = readVersion();
  const tag = `v${version}`;
  const repo = process.env.WP_SSH_BRIDGE_REPO || "Citation-Media/wp-ssh-bridge";
  const { os, arch } = target();
  const archiveName = `wp-ssh-bridge_${tag}_${os}_${arch}.tar.gz`;
  const base = `https://github.com/${repo}/releases/download/${tag}`;

  log(`Downloading wp-ssh-bridge ${tag} for ${os}/${arch}`);
  // The archive is fetched first so a missing release reports the archive URL
  // rather than its checksum file, which reads as the wrong problem.
  const archive = await download(`${base}/${archiveName}`);
  const checksums = (await download(`${base}/checksums.txt`)).toString("utf8");

  const expected = expectedDigest(checksums, archiveName);
  const actual = createHash("sha256").update(archive).digest("hex");
  if (expected !== actual) {
    throw new Error(`checksum mismatch for ${archiveName} (expected ${expected}, got ${actual})`);
  }

  const staging = mkdtempSync(join(tmpdir(), "wp-ssh-bridge-"));
  try {
    const archivePath = join(staging, archiveName);
    writeFileSync(archivePath, archive);
    const extracted = spawnSync("tar", ["-xzf", archivePath, "-C", staging, "wp-ssh-bridge"], {
      stdio: "inherit",
    });
    if (extracted.error) throw extracted.error;
    if (extracted.status !== 0) throw new Error("tar could not extract the archive");

    const unpacked = join(staging, "wp-ssh-bridge");
    if (!existsSync(unpacked)) throw new Error("the archive contained no wp-ssh-bridge binary");
    chmodSync(unpacked, 0o755);

    mkdirSync(dirname(binaryPath), { recursive: true });
    rmSync(binaryPath, { force: true });
    renameSync(unpacked, binaryPath);
  } finally {
    rmSync(staging, { recursive: true, force: true });
  }

  log(`Installed ${binaryPath}`);
  return binaryPath;
}

// Used by the bin shim: return a usable binary, downloading it only if needed.
// This is what makes the package survive an install run with --ignore-scripts.
export async function ensure({ log = () => {} } = {}) {
  const override = overriddenBinary();
  if (override) return override;
  if (existsSync(binaryPath)) return binaryPath;
  return install({ log });
}

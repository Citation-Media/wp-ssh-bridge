// Refuses to publish a version whose GitHub release cannot serve the binary.
//
// The package downloads its binary from the release tagged v<version>. Publish a
// version before that release exists, or while the repository is private, and
// every install of it fails at postinstall. This runs as prepublishOnly, so it
// guards the manual first publish and the CI job alike.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

if (process.env.WP_SSH_BRIDGE_SKIP_RELEASE_CHECK) {
  process.exit(0);
}

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const { version } = JSON.parse(readFileSync(join(packageRoot, "package.json"), "utf8"));
const tag = `v${version}`;
const repo = process.env.WP_SSH_BRIDGE_REPO || "Citation-Media/wp-ssh-bridge";
const base = `https://github.com/${repo}/releases/download/${tag}`;

const assets = ["checksums.txt"];
for (const os of ["darwin", "linux"]) {
  for (const arch of ["amd64", "arm64"]) {
    assets.push(`wp-ssh-bridge_${tag}_${os}_${arch}.tar.gz`);
  }
}

const missing = [];
for (const asset of assets) {
  const url = `${base}/${asset}`;
  try {
    const response = await fetch(url, { method: "HEAD", redirect: "follow" });
    if (!response.ok) missing.push(`${asset} (HTTP ${response.status})`);
  } catch (error) {
    missing.push(`${asset} (${error.message})`);
  }
}

if (missing.length > 0) {
  process.stderr.write(
    `\nRefusing to publish ${tag}: the GitHub release cannot serve the binary.\n\n` +
      missing.map((asset) => `  missing: ${asset}`).join("\n") +
      `\n\nChecked ${base}\n` +
      "Publish only after that release exists and the repository is public, otherwise\n" +
      "every install of this version fails. Set WP_SSH_BRIDGE_SKIP_RELEASE_CHECK=1 to override.\n",
  );
  process.exit(1);
}

process.stdout.write(`Release ${tag} serves all ${assets.length} assets.\n`);

// Fetches the binary at install time so the first command runs without a wait.
//
// This never fails the install. The binary is also fetched on demand by the bin
// shim, so a blocked postinstall, an offline machine, or an install from a
// checkout without a matching release all degrade to a download later instead
// of a broken `npm install`.
import { install, overriddenBinary } from "../lib/binary.js";

if (process.env.WP_SSH_BRIDGE_SKIP_DOWNLOAD) {
  process.exit(0);
}

try {
  if (overriddenBinary()) process.exit(0);
  await install({ log: (message) => process.stdout.write(`${message}\n`) });
} catch (error) {
  process.stdout.write(
    `wp-ssh-bridge: could not download the binary now (${error.message}).\n` +
      "It will be downloaded the first time you run wp-ssh-bridge.\n",
  );
}

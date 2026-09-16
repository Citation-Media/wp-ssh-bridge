#!/usr/bin/env node
// Thin shim: make sure the binary for this platform is present, then hand the
// process over to it. Arguments, stdio, exit code, and signals pass straight
// through, so `npx wp-ssh-bridge init` behaves exactly like the binary does,
// interactive prompts included.
import { spawn } from "node:child_process";
import { ensure } from "../lib/binary.js";

const args = process.argv.slice(2);

let binary;
try {
  // Downloading here rather than only in postinstall keeps the package working
  // when scripts were skipped at install time, which pnpm does by default.
  binary = await ensure({ log: (message) => process.stderr.write(`${message}\n`) });
} catch (error) {
  process.stderr.write(`wp-ssh-bridge: ${error.message}\n`);
  process.exit(1);
}

const child = spawn(binary, args, { stdio: "inherit" });

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("error", (error) => {
  process.stderr.write(`wp-ssh-bridge: could not run ${binary}: ${error.message}\n`);
  process.exit(1);
});

// A process killed by a signal reports 128 + the signal number, the same
// convention a shell uses, so CI sees the real outcome either way.
const SIGNAL_NUMBERS = { SIGINT: 2, SIGTERM: 15, SIGHUP: 1 };

child.on("close", (code, signal) => {
  if (signal) {
    process.exit(128 + (SIGNAL_NUMBERS[signal] ?? 0));
  }
  process.exit(code ?? 1);
});

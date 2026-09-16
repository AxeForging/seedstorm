// Stops the server started by global.setup.ts and drops every scratch database.
// Set SEEDSTORM_E2E_KEEP=1 to keep the databases and temp dir for debugging.
import fs from "node:fs";
import { dropAllDatabases } from "./databases.fixture";
import type { E2EState } from "./global.setup";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function stop(pid: number): Promise<void> {
  if (!alive(pid)) return;
  process.kill(pid, "SIGTERM");
  const deadline = Date.now() + 10_000;
  while (alive(pid) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
  if (alive(pid)) process.kill(pid, "SIGKILL");
}

export default async function globalTeardown(): Promise<void> {
  const statePath = process.env.SEEDSTORM_E2E_STATE;
  if (!statePath || !fs.existsSync(statePath)) return;
  const state = JSON.parse(fs.readFileSync(statePath, "utf8")) as E2EState;
  await stop(state.pid);
  if (process.env.SEEDSTORM_E2E_KEEP) {
    console.log(`kept scratch databases and ${state.tmpDir}`);
    return;
  }
  await dropAllDatabases();
  fs.rmSync(state.tmpDir, { recursive: true, force: true });
}

// Builds the binary, creates the scratch databases and starts `seedstorm serve`
// on a free port with its config (saved connections, profiles) in a temp dir.
// The URL reaches the tests through SEEDSTORM_E2E_URL; teardown reads the rest
// from the state file.
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { createAllDatabases } from "./databases.fixture";

export const REPO_ROOT = path.resolve(__dirname, "..", "..");

export interface E2EState {
  tmpDir: string;
  pid: number;
  url: string;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address() as net.AddressInfo;
      srv.close(() => resolve(port));
    });
  });
}

async function waitForServer(url: string, child: ReturnType<typeof spawn>, logs: () => string): Promise<void> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`seedstorm serve exited with ${child.exitCode}:\n${logs()}`);
    try {
      const res = await fetch(url + "/connect");
      if (res.ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`seedstorm serve did not answer on ${url} within 30s:\n${logs()}`);
}

export default async function globalSetup(): Promise<void> {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "seedstorm-e2e-"));
  let bin = process.env.SEEDSTORM_E2E_BIN;
  if (!bin) {
    bin = path.join(tmpDir, "seedstorm");
    try {
      execFileSync("go", ["build", "-o", bin, "./cmd/seedstorm"], { cwd: REPO_ROOT, stdio: "inherit" });
    } catch (err) {
      fs.rmSync(tmpDir, { recursive: true, force: true });
      throw err;
    }
  }

  await createAllDatabases();

  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const configHome = path.join(tmpDir, "config");
  fs.mkdirSync(configHome, { recursive: true });
  const logPath = path.join(tmpDir, "serve.log");
  const log = fs.openSync(logPath, "a");
  const child = spawn(bin, ["serve", "--addr", `127.0.0.1:${port}`], {
    env: { ...process.env, XDG_CONFIG_HOME: configHome, SEEDSTORM_PROFILES: path.join(tmpDir, "profiles.yaml") },
    stdio: ["ignore", log, log],
  });
  const state: E2EState = { tmpDir, pid: child.pid!, url };
  const statePath = path.join(tmpDir, "state.json");
  fs.writeFileSync(statePath, JSON.stringify(state));
  process.env.SEEDSTORM_E2E_STATE = statePath;
  process.env.SEEDSTORM_E2E_URL = url;
  process.env.SEEDSTORM_E2E_SERVE_LOG = logPath;

  try {
    await waitForServer(url, child, () => fs.readFileSync(logPath, "utf8"));
  } catch (err) {
    child.kill("SIGKILL");
    throw err;
  }
}

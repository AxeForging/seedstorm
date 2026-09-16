// Database access for fixtures and assertions. Everything the suite creates is
// named ss_e2e_* so it never touches databases other test runs own.
import { Client } from "pg";
import mysql from "mysql2/promise";

const env = (name: string, fallback: string) => process.env[name] || fallback;

export const pg = {
  host: env("SEEDSTORM_PG_HOST", "localhost"),
  port: Number(env("SEEDSTORM_PG_PORT", "5432")),
  user: "seedstorm",
  password: "seedstorm",
};

export const my = {
  host: env("SEEDSTORM_MYSQL_HOST", "localhost"),
  port: Number(env("SEEDSTORM_MYSQL_PORT", "3306")),
  rootPassword: env("SEEDSTORM_MYSQL_ROOT_PASSWORD", "root"),
};

export const DB = {
  src: "ss_e2e_src",
  tgt: "ss_e2e_tgt",
  clone: "ss_e2e_clone",
  wide: "ss_e2e_wide",
  mysqlReadOnly: "ss_e2e_ro",
} as const;

export const MYSQL_READER = { user: "ss_e2e_reader", password: "ss_e2e_pw" };

export function pgDSN(database: string): string {
  return `postgres://${pg.user}:${pg.password}@${pg.host}:${pg.port}/${database}?sslmode=disable`;
}

// withPg runs fn on a connection to database and always closes it.
export async function withPg<T>(database: string, fn: (c: Client) => Promise<T>): Promise<T> {
  const client = new Client({ ...pg, database });
  await client.connect();
  try {
    return await fn(client);
  } finally {
    await client.end();
  }
}

export async function pgQuery<T = Record<string, unknown>>(database: string, sql: string, params: unknown[] = []): Promise<T[]> {
  return withPg(database, async (c) => (await c.query(sql, params)).rows as T[]);
}

// pgCounts returns exact row counts for every table in schema public.
export async function pgCounts(database: string, tables?: string[]): Promise<Record<string, number>> {
  return withPg(database, async (c) => {
    const names = tables ?? (await c.query<{ tablename: string }>(
      "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename",
    )).rows.map((r) => r.tablename);
    const out: Record<string, number> = {};
    for (const name of names) {
      const { rows } = await c.query<{ n: string }>(`SELECT count(*) AS n FROM "${name}"`);
      out[name] = Number(rows[0].n);
    }
    return out;
  });
}

// recreatePgDatabase drops (terminating open connections) and creates database.
export async function recreatePgDatabase(database: string): Promise<void> {
  assertScratch(database);
  await withPg("testdb", async (c) => {
    await c.query(`DROP DATABASE IF EXISTS ${database} WITH (FORCE)`);
    // Another run creating a database at the same moment holds template1.
    for (let attempt = 1; ; attempt++) {
      try {
        await c.query(`CREATE DATABASE ${database}`);
        return;
      } catch (err) {
        if (attempt >= 20 || !String(err).includes("being accessed by other users")) throw err;
        await new Promise((r) => setTimeout(r, 250 * attempt));
      }
    }
  });
}

export async function dropPgDatabase(database: string): Promise<void> {
  assertScratch(database);
  await withPg("testdb", (c) => c.query(`DROP DATABASE IF EXISTS ${database} WITH (FORCE)`));
}

export async function withMysqlRoot<T>(fn: (c: mysql.Connection) => Promise<T>): Promise<T> {
  const conn = await mysql.createConnection({
    host: my.host, port: my.port, user: "root", password: my.rootPassword, multipleStatements: true,
  });
  try {
    return await fn(conn);
  } finally {
    await conn.end();
  }
}

function assertScratch(database: string) {
  if (!database.startsWith("ss_e2e_")) throw new Error(`refusing to touch ${database}: not an ss_e2e_* scratch database`);
}

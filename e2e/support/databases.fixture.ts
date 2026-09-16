// Scratch databases the journeys run against. Each builder recreates its
// database from scratch, so a spec can reset the state it writes to.
import { DB, MYSQL_READER, dropPgDatabase, recreatePgDatabase, withMysqlRoot, withPg } from "./db.helpers";
import { wideSchema } from "./wide-schema.fixture";

export const SOURCE_ROWS = { customers: 1200, orders: 3400 };

const TABLES_DDL = [
  "CREATE TABLE customers (id SERIAL PRIMARY KEY, name TEXT NOT NULL, tier TEXT)",
  "CREATE TABLE orders (id SERIAL PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(id), amount INTEGER NOT NULL, status TEXT)",
];

// The source carries objects clone-schema must reproduce: a view on a view, a
// SQL function and a trigger backed by a plpgsql function.
const OBJECTS_DDL = [
  "CREATE FUNCTION add_tax(v integer) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT v + 10 $$",
  `CREATE FUNCTION orders_default_status() RETURNS trigger LANGUAGE plpgsql AS $$
   BEGIN
     IF NEW.status IS NULL THEN NEW.status := 'new'; END IF;
     RETURN NEW;
   END $$`,
  "CREATE VIEW order_totals AS SELECT customer_id, SUM(amount)::int AS total FROM orders GROUP BY customer_id",
  "CREATE VIEW a_big_spenders AS SELECT c.name, t.total FROM customers c JOIN order_totals t ON t.customer_id = c.id WHERE t.total > 100",
  "CREATE TRIGGER trg_orders_status BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION orders_default_status()",
];

export async function createSourceDatabase(): Promise<void> {
  await recreatePgDatabase(DB.src);
  await withPg(DB.src, async (c) => {
    for (const stmt of [...TABLES_DDL, ...OBJECTS_DDL]) await c.query(stmt);
    await c.query(`INSERT INTO customers (name, tier) SELECT 'customer ' || g, CASE WHEN g % 3 = 0 THEN 'gold' END FROM generate_series(1, $1) g`, [SOURCE_ROWS.customers]);
    await c.query(`INSERT INTO orders (customer_id, amount) SELECT 1 + (g % $1), 10 + g % 400 FROM generate_series(1, $2) g`, [SOURCE_ROWS.customers, SOURCE_ROWS.orders]);
    await c.query("ANALYZE");
  });
}

// The mirror target has the source's tables and nothing else, empty.
export async function createTargetDatabase(): Promise<void> {
  await recreatePgDatabase(DB.tgt);
  await withPg(DB.tgt, async (c) => {
    for (const stmt of TABLES_DDL) await c.query(stmt);
  });
}

export async function createCloneTargetDatabase(): Promise<void> {
  await recreatePgDatabase(DB.clone);
}

export const WIDE = wideSchema(150, "postgres");

export async function createWideDatabase(): Promise<void> {
  await recreatePgDatabase(DB.wide);
  await withPg(DB.wide, async (c) => {
    await c.query("BEGIN");
    for (const stmt of WIDE.statements) await c.query(stmt);
    await c.query("COMMIT");
  });
}

// A MySQL database the reader account may only SELECT from.
export async function createMysqlReadOnly(): Promise<void> {
  const { user, password } = MYSQL_READER;
  await withMysqlRoot(async (c) => {
    await c.query(`DROP DATABASE IF EXISTS \`${DB.mysqlReadOnly}\``);
    await c.query(`DROP USER IF EXISTS '${user}'@'%'`);
    await c.query(`CREATE DATABASE \`${DB.mysqlReadOnly}\``);
    await c.query(`CREATE TABLE \`${DB.mysqlReadOnly}\`.warehouses (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL)`);
    await c.query(`CREATE TABLE \`${DB.mysqlReadOnly}\`.parcels (id INT PRIMARY KEY, warehouse_id INT NOT NULL, FOREIGN KEY (warehouse_id) REFERENCES warehouses(id))`);
    await c.query(`CREATE USER '${user}'@'%' IDENTIFIED BY '${password}'`);
    await c.query(`GRANT SELECT ON \`${DB.mysqlReadOnly}\`.* TO '${user}'@'%'`);
  });
}

export async function createAllDatabases(): Promise<void> {
  // CREATE DATABASE copies template1, which refuses concurrent copies: one at a time.
  for (const create of [createSourceDatabase, createTargetDatabase, createCloneTargetDatabase, createWideDatabase]) await create();
  await createMysqlReadOnly();
}

export async function dropAllDatabases(): Promise<void> {
  const errors: unknown[] = [];
  const attempt = (p: Promise<unknown>) => p.catch((err) => errors.push(err));
  await Promise.all([
    ...[DB.src, DB.tgt, DB.clone, DB.wide].map((name) => attempt(dropPgDatabase(name))),
    attempt(withMysqlRoot(async (c) => {
      await c.query(`DROP DATABASE IF EXISTS \`${DB.mysqlReadOnly}\``);
      await c.query(`DROP USER IF EXISTS '${MYSQL_READER.user}'@'%'`);
    })),
  ]);
  if (errors.length) throw new AggregateError(errors, "dropping e2e databases");
}

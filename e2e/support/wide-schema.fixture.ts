// A TypeScript port of wideSchemaDDL (integration/wide_schema_test.go): a
// deterministic schema of n tables shaped like a large product database. Every
// table references up to three earlier ones (the first NOT NULL, the rest
// nullable), every fifth references itself, every seventh gets a nullable FK to
// a later table once all tables exist (a near-cycle), and every eleventh is a
// junction keyed by two non-junction parents. The Go generator uses math/rand;
// this one uses a seeded mulberry32, so the shape matches and the output is
// stable across runs, though the exact references differ from the Go fixture.

export const WIDE_NOUNS = [
  "account", "invoice", "shipment", "product", "region", "warehouse", "ticket", "booking",
  "vessel", "berth", "crane", "driver", "truck", "slot", "gate", "container",
];

export function wideTableName(i: number): string {
  return `t${String(i).padStart(3, "0")}_${WIDE_NOUNS[i % WIDE_NOUNS.length]}`;
}

export function isJunction(i: number): boolean {
  return i % 11 === 10 && i >= 2;
}

function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export interface WideSchema {
  statements: string[];
  // hardParents maps a table to the tables its NOT NULL foreign keys reference.
  hardParents: Map<string, string[]>;
}

export function wideSchema(n: number, driver: "postgres" | "mysql" = "postgres"): WideSchema {
  const rand = mulberry32(20260916);
  const intn = (max: number) => Math.floor(rand() * max);
  const serial = driver === "mysql" ? "INTEGER AUTO_INCREMENT" : "SERIAL";
  const creates: string[] = [];
  const later: string[] = [];
  const hardParents = new Map<string, string[]>();

  for (let i = 0; i < n; i++) {
    const t = wideTableName(i);
    if (isJunction(i)) {
      // Junction parents are regular tables: junctions have no id.
      const pick = () => {
        for (;;) {
          const p = intn(i);
          if (p % 11 !== 10) return p;
        }
      };
      const a = pick();
      let b = pick();
      while (b === a) b = pick();
      hardParents.set(t, [wideTableName(a), wideTableName(b)]);
      creates.push(`CREATE TABLE ${t} (
  left_id INTEGER NOT NULL, right_id INTEGER NOT NULL, weight INTEGER NOT NULL,
  PRIMARY KEY (left_id, right_id),
  FOREIGN KEY (left_id) REFERENCES ${wideTableName(a)}(id), FOREIGN KEY (right_id) REFERENCES ${wideTableName(b)}(id))`);
      continue;
    }
    const cols = [`id ${serial} PRIMARY KEY`, "label VARCHAR(60) NOT NULL", "amount NUMERIC(10,2)", "created_at TIMESTAMP NULL"];
    const fks: string[] = [];
    const seen = new Set<number>();
    const hard: string[] = [];
    const refs = Math.min(i, 1 + intn(3));
    for (let k = 0; k < refs; k++) {
      const p = intn(i);
      if (seen.has(p) || p % 11 === 10) continue;
      seen.add(p);
      const col = `${wideTableName(p)}_id`;
      const nullable = k > 0;
      if (!nullable) hard.push(wideTableName(p));
      cols.push(`${col} INTEGER ${nullable ? "NULL" : "NOT NULL"}`);
      fks.push(`FOREIGN KEY (${col}) REFERENCES ${wideTableName(p)}(id)`);
    }
    if (i % 5 === 4) {
      cols.push("parent_id INTEGER NULL");
      fks.push(`FOREIGN KEY (parent_id) REFERENCES ${t}(id)`);
    }
    hardParents.set(t, hard);
    creates.push(`CREATE TABLE ${t} (\n  ${[...cols, ...fks].join(",\n  ")}\n)`);
    if (i % 7 === 6 && i + 3 < n && (i + 3) % 11 !== 10) {
      later.push(
        `ALTER TABLE ${t} ADD COLUMN later_ref_id INTEGER NULL`,
        `ALTER TABLE ${t} ADD FOREIGN KEY (later_ref_id) REFERENCES ${wideTableName(i + 3)}(id)`,
      );
    }
  }
  return { statements: [...creates, ...later], hardParents };
}

// requiredClosure is table plus every table reachable through NOT NULL
// foreign keys: what the workspace auto-locks when table is selected.
export function requiredClosure(schema: WideSchema, table: string): Set<string> {
  const out = new Set<string>([table]);
  const queue = [table];
  while (queue.length) {
    for (const p of schema.hardParents.get(queue.shift()!) ?? []) {
      if (p === table || out.has(p)) continue;
      out.add(p);
      queue.push(p);
    }
  }
  return out;
}

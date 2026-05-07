# Schema YAML Format

The schema file is produced by `introspect` and consumed by `seed`, `generate`, and `gaps`.

## Example

```yaml
tables:
  users:
    columns:
      id:
        type: integer
        pk: true
      email:
        type: varchar
        faker: email
      first_name:
        type: varchar
        faker: firstname
      created_at:
        type: timestamp
        faker: datetime
  orders:
    columns:
      id:
        type: integer
        pk: true
      user_id:
        type: integer
        fk: users.id        # FK resolved automatically
      status:
        type: varchar
        faker: randomstring(pending,processing,shipped,delivered)
      total_amount:
        type: numeric
        faker: price(10,500)
```

## Column fields

| Field | Description |
|-------|-------------|
| `type` | SQL type (`integer`, `varchar`, `numeric`, `boolean`, `timestamp`, `uuid`, …) |
| `pk` | `true` if this column is a primary key |
| `fk` | Foreign key reference in `table.column` format |
| `faker` | Faker hint (see table below). Auto-assigned by `introspect`; overridden by `ai-enrich`. |
| `nullable` | `true` if the column allows NULL |
| `unique` | `true` if the column has a UNIQUE constraint (auto-sets faker to `uuid`) |

## Faker Hints Reference

| Hint | Generates |
|------|-----------|
| `email` | `user@example.com` |
| `firstname` / `lastname` / `name` | Person names |
| `username` | `CoolUser42` |
| `phone` | `+1-555-0123` |
| `city` / `country` / `state` / `zip` | Geographic data |
| `street` | Street address |
| `url` | `https://example.com/path` |
| `uuid` | `550e8400-e29b-41d4-a716-446655440000` |
| `price(min,max)` | `42.99` |
| `number(min,max)` | Integer in range |
| `float64` | Random float |
| `latitude` / `longitude` | Coordinate values |
| `sentence` | Short sentence |
| `paragraph(N)` | N-paragraph text |
| `bool` | `true` / `false` |
| `datetime` / `date` / `time` | Temporal values |
| `json` | `{"key":"word","value":"word"}` |
| `ipv4` | `192.168.1.42` |
| `company` | Company name |
| `productname` | Product name |
| `randomstring(a,b,c)` | Random pick from list — also used for CHECK IN constraints |

## Constraint auto-detection

`introspect` sets faker hints automatically from DB constraints:

| Constraint type | Example | Assigned faker |
|-----------------|---------|---------------|
| UNIQUE | `users.email` | `uuid` |
| CHECK IN | `status IN ('active','inactive')` | `randomstring(active,inactive)` |
| CHECK range | `rating BETWEEN 1 AND 5` | `number(1,5)` |

These auto-assignments can be refined further with `ai-enrich`.

## Tips

- Run `introspect` to generate the base schema, then `ai-enrich` to improve faker quality.
- You can hand-edit the schema YAML to override any faker hint before seeding.
- `randomstring(a,b,c)` with 12 or fewer values triggers enum coverage — every value appears at least `--rows` times.
- Self-referential FKs (`categories.parent_id → categories.id`) are detected and handled automatically.

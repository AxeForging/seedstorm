package faker

import "math/rand"

// poolLimit bounds how many primary-key values are kept per table for foreign
// keys to pick from. Larger tables are sampled uniformly, so children still
// spread across the whole parent table while memory stays flat.
var poolLimit = 500_000

// poolSampler builds a PK pool with reservoir sampling. For integer keys the
// largest value always ends the pool: nextSequentialPK reads it to continue
// ids past every stored row, sampled or not.
type poolSampler struct {
	pool    []interface{}
	limit   int
	seen    int
	allInts bool
	max     int64
}

func newPoolSampler(initial []interface{}, limit int) *poolSampler {
	p := &poolSampler{pool: initial, limit: limit, allInts: true}
	for _, v := range initial {
		p.track(v)
	}
	p.seen = len(initial)
	return p
}

func (p *poolSampler) track(v interface{}) {
	if !p.allInts {
		return
	}
	n, ok := asInt(v)
	if !ok {
		p.allInts = false
		return
	}
	if n > p.max {
		p.max = n
	}
}

func (p *poolSampler) offer(v interface{}) {
	p.track(v)
	p.seen++
	if len(p.pool) < p.limit {
		p.pool = append(p.pool, v)
		return
	}
	if j := rand.Intn(p.seen); j < p.limit { //nolint:gosec // sampling test data
		p.pool[j] = v
	}
}

func (p *poolSampler) values() []interface{} {
	if !p.allInts || len(p.pool) == 0 {
		return p.pool
	}
	sortIntPool(p.pool)
	if last, _ := asInt(p.pool[len(p.pool)-1]); last < p.max {
		p.pool[len(p.pool)-1] = p.max
	}
	return p.pool
}

// capPool shrinks a pool that grew past limit during generation to a uniform
// sample, keeping its last value (the largest generated id) at the end. It
// returns a new slice so the old backing array can be freed.
func capPool(pool []interface{}, limit int) []interface{} {
	if limit <= 0 || len(pool) <= limit {
		return pool
	}
	body := pool[:len(pool)-1]
	for i := 0; i < limit-1; i++ {
		j := i + rand.Intn(len(body)-i) //nolint:gosec // sampling test data
		body[i], body[j] = body[j], body[i]
	}
	out := make([]interface{}, limit)
	copy(out, body[:limit-1])
	out[limit-1] = pool[len(pool)-1]
	return out
}

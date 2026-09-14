package faker

import "hash/fnv"

// DefaultExactKeys is how many keys a set keeps exactly before it switches to a
// Bloom filter (roughly 20MB of map against 2MB of filter per million keys).
const DefaultExactKeys = 200_000

// keySet records keys already taken (stored rows, earlier chunks). Small sets
// are exact maps. Past limit they become a scalable Bloom filter: about 10 bits
// per key instead of a map entry, Has never misses a key that was added (so no
// duplicate can slip through), and a rare false positive only makes the
// generator skip a value that was free.
type keySet struct {
	limit  int
	exact  map[string]bool
	filter *scalableBloom
	n      int
}

func newKeySet(limit int) *keySet {
	if limit <= 0 {
		limit = DefaultExactKeys
	}
	return &keySet{limit: limit, exact: map[string]bool{}}
}

// keysOf builds an exact set, mainly for tests.
func keysOf(keys ...string) *keySet {
	ks := newKeySet(0)
	for _, k := range keys {
		ks.Add(k)
	}
	return ks
}

func (ks *keySet) Has(key string) bool {
	if ks == nil {
		return false
	}
	if ks.filter != nil {
		return ks.filter.has(key)
	}
	return ks.exact[key]
}

func (ks *keySet) Add(key string) {
	if ks.filter != nil {
		ks.filter.add(key)
		ks.n++
		return
	}
	if ks.exact[key] {
		return
	}
	ks.exact[key] = true
	ks.n++
	if len(ks.exact) > ks.limit {
		ks.filter = newScalableBloom(2 * ks.limit)
		for k := range ks.exact {
			ks.filter.add(k)
		}
		ks.exact = nil
	}
}

func (ks *keySet) Len() int {
	if ks == nil {
		return 0
	}
	return ks.n
}

// takenKeys reports keys already used: a keySet, or a seen layered over one.
type takenKeys interface {
	Has(key string) bool
	Len() int
}

func hasKey(t takenKeys, key string) bool { return t != nil && t.Has(key) }

func keyCount(t takenKeys) int {
	if t == nil {
		return 0
	}
	return t.Len()
}

// seen layers keys added during one generation call over a shared set, so the
// shared set is never copied.
type seen struct {
	base  takenKeys
	local map[string]bool
}

func newSeen(base takenKeys) *seen { return &seen{base: base, local: map[string]bool{}} }

func (s *seen) Has(key string) bool { return s.local[key] || hasKey(s.base, key) }

func (s *seen) Add(key string) { s.local[key] = true }

func (s *seen) Len() int { return len(s.local) + keyCount(s.base) }

// scalableBloom chains filters, each twice as large as the one before, so the
// false-positive rate stays low however many keys arrive.
type scalableBloom struct {
	filters []*bloomFilter
}

func newScalableBloom(capacity int) *scalableBloom {
	return &scalableBloom{filters: []*bloomFilter{newBloomFilter(capacity)}}
}

func (s *scalableBloom) add(key string) {
	last := s.filters[len(s.filters)-1]
	if last.count >= last.capacity {
		last = newBloomFilter(2 * last.capacity)
		s.filters = append(s.filters, last)
	}
	last.add(key)
}

func (s *scalableBloom) has(key string) bool {
	for _, f := range s.filters {
		if f.has(key) {
			return true
		}
	}
	return false
}

// bytes reports the filter's memory.
func (s *scalableBloom) bytes() int {
	total := 0
	for _, f := range s.filters {
		total += len(f.bits) * 8
	}
	return total
}

type bloomFilter struct {
	bits     []uint64
	m        uint64
	capacity int
	count    int
}

// bloomHashes probes per key and bloomBitsPerKey keep each full layer near a
// 0.15% false-positive rate.
const (
	bloomHashes     = 7
	bloomBitsPerKey = 14
)

func newBloomFilter(capacity int) *bloomFilter {
	m := uint64(capacity) * bloomBitsPerKey
	if m < 1024 {
		m = 1024
	}
	return &bloomFilter{bits: make([]uint64, (m+63)/64), m: m, capacity: capacity}
}

func bloomPositions(key string) (uint64, uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	sum := h.Sum64()
	return sum, (sum >> 33) | 1
}

func (b *bloomFilter) add(key string) {
	b.count++
	h1, h2 := bloomPositions(key)
	for i := uint64(0); i < bloomHashes; i++ {
		pos := (h1 + i*h2) % b.m
		b.bits[pos/64] |= 1 << (pos % 64)
	}
}

func (b *bloomFilter) has(key string) bool {
	h1, h2 := bloomPositions(key)
	for i := uint64(0); i < bloomHashes; i++ {
		pos := (h1 + i*h2) % b.m
		if b.bits[pos/64]&(1<<(pos%64)) == 0 {
			return false
		}
	}
	return true
}

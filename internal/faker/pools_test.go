package faker

import (
	"math/rand"
	"testing"
)

func TestPoolSampler_LargeTableIsSampledButKeepsTheMaximumLast(t *testing.T) {
	const total, limit = 10_000, 100
	order := rand.Perm(total)
	p := newPoolSampler(nil, limit, defaultGen.rnd)
	for _, n := range order {
		p.offer(int64(n + 1))
	}
	pool := p.values()
	if len(pool) != limit {
		t.Fatalf("pool has %d values, want %d", len(pool), limit)
	}
	if last, _ := asInt(pool[len(pool)-1]); last != total {
		t.Fatalf("last value = %d, want the maximum %d so new ids continue past it", last, total)
	}
	if next := nextSequentialPK(pool); next != total {
		t.Fatalf("nextSequentialPK = %d, want %d", next, total)
	}
	low, high := 0, 0
	distinct := map[int64]bool{}
	for _, v := range pool {
		n, _ := asInt(v)
		if n < 1 || n > total || distinct[n] {
			t.Fatalf("value %d is out of range or repeated", n)
		}
		distinct[n] = true
		if n <= total/2 {
			low++
		} else {
			high++
		}
	}
	if low == 0 || high == 0 {
		t.Fatalf("sample is not spread over the table: %d low, %d high", low, high)
	}
}

func TestPoolSampler_SmallTableKeepsEveryValue(t *testing.T) {
	p := newPoolSampler([]interface{}{int64(9)}, 100, defaultGen.rnd)
	for _, n := range []int64{3, 7, 1} {
		p.offer(n)
	}
	got := p.values()
	want := []int64{1, 3, 7, 9}
	if len(got) != len(want) {
		t.Fatalf("pool = %v, want %v", got, want)
	}
	for i := range want {
		if n, _ := asInt(got[i]); n != want[i] {
			t.Fatalf("pool = %v, want %v", got, want)
		}
	}
}

func TestCapPool_ShrinksToLimitKeepingTheLastID(t *testing.T) {
	pool := make([]interface{}, 1000)
	for i := range pool {
		pool[i] = i + 1
	}
	capped := capPool(pool, 10, defaultGen.rnd)
	if len(capped) != 10 || capped[9] != 1000 {
		t.Fatalf("capped = %v, want 10 values ending with 1000", capped)
	}
	seen := map[interface{}]bool{}
	for _, v := range capped {
		if seen[v] {
			t.Fatalf("value %v repeated in %v", v, capped)
		}
		seen[v] = true
	}
	if small := []interface{}{1, 2}; len(capPool(small, 10, defaultGen.rnd)) != 2 {
		t.Fatal("a pool under the limit must be returned unchanged")
	}
}

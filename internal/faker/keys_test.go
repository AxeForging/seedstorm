package faker

import (
	"fmt"
	"testing"
)

func TestKeySet_ExactBelowLimit(t *testing.T) {
	ks := newKeySet(10)
	ks.Add("a")
	if !ks.Has("a") || ks.Has("b") || ks.Len() != 1 || ks.filter != nil {
		t.Fatalf("exact set misbehaves: %+v", ks)
	}
	var nilSet *keySet
	if nilSet.Has("a") || nilSet.Len() != 0 {
		t.Fatal("nil set must be empty")
	}
}

func TestKeySet_PromotedFilterNeverForgetsAKeyAndStaysBounded(t *testing.T) {
	const limit = 1000
	ks := newKeySet(limit)
	for i := 0; i < 50*limit; i++ {
		ks.Add(fmt.Sprintf("id=%d", i))
	}
	if ks.exact != nil || ks.filter == nil {
		t.Fatal("set was not promoted past its limit")
	}
	for i := 0; i < 50*limit; i++ {
		if !ks.Has(fmt.Sprintf("id=%d", i)) {
			t.Fatalf("filter lost key id=%d: a false negative would allow duplicates", i)
		}
	}
	falsePositives := 0
	for i := 0; i < 10000; i++ {
		if ks.Has(fmt.Sprintf("absent=%d", i)) {
			falsePositives++
		}
	}
	// A false positive only skips a free value; keep them rare.
	if falsePositives > 500 {
		t.Fatalf("%d false positives in 10000 lookups", falsePositives)
	}
	if bytes := ks.filter.bytes(); bytes > 1<<20 {
		t.Fatalf("filter uses %d bytes for 50k keys", bytes)
	}
}

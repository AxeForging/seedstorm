package web

import (
	"sort"
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/relations"
)

// shapeCache holds a session's relationship shapes as a scan measures them, so
// the workspace can show each one as it finishes and export them later.
type shapeCache struct {
	mu       sync.Mutex
	gen      int
	byKey    map[string]relations.Shape
	total    int
	running  bool
	takenAt  time.Time
	estimate bool
}

// shapeView is what /api/relationships returns.
type shapeView struct {
	Shapes   []relations.Shape `json:"shapes"`
	Total    int               `json:"total"`
	Running  bool              `json:"running"`
	TakenAt  string            `json:"takenAt,omitempty"`
	Estimate bool              `json:"estimate,omitempty"`
}

// begin starts a new scan, dropping earlier shapes; the returned generation
// keeps a superseded scan from writing into the new one.
func (c *shapeCache) begin(estimate bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.byKey, c.total, c.running, c.takenAt, c.estimate = map[string]relations.Shape{}, 0, true, time.Now().UTC(), estimate
	return c.gen
}

func (c *shapeCache) put(gen, total int, s relations.Shape) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.total = total
	c.byKey[s.Child+"."+s.Column] = s
}

func (c *shapeCache) finish(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		c.running = false
	}
}

// reset forgets shapes: a run wrote to the database.
func (c *shapeCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.byKey, c.total, c.running, c.takenAt = nil, 0, false, time.Time{}
}

func (c *shapeCache) view() shapeView {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := shapeView{Shapes: make([]relations.Shape, 0, len(c.byKey)), Total: c.total, Running: c.running, Estimate: c.estimate}
	for _, s := range c.byKey {
		v.Shapes = append(v.Shapes, s)
	}
	sort.Slice(v.Shapes, func(i, j int) bool {
		if v.Shapes[i].Child != v.Shapes[j].Child {
			return v.Shapes[i].Child < v.Shapes[j].Child
		}
		return v.Shapes[i].Column < v.Shapes[j].Column
	})
	if !c.takenAt.IsZero() {
		v.TakenAt = c.takenAt.Format(time.RFC3339)
	}
	return v
}

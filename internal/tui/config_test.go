package tui

import "testing"

func TestConfig_defaultValues(t *testing.T) {
	c := newConfig(50, 200, 10, false)
	if c.Rows() != 50 {
		t.Errorf("Rows() = %d, want 50", c.Rows())
	}
	if c.BatchSize() != 200 {
		t.Errorf("BatchSize() = %d, want 200", c.BatchSize())
	}
	if c.EnumRows() != 10 {
		t.Errorf("EnumRows() = %d, want 10", c.EnumRows())
	}
	if c.Truncate() {
		t.Error("Truncate() = true, want false")
	}
}

func TestConfig_truncateToggle(t *testing.T) {
	c := newConfig(100, 100, 0, false)
	if c.Truncate() {
		t.Fatal("should start false")
	}
	c2 := newConfig(100, 100, 0, true)
	if !c2.Truncate() {
		t.Fatal("should start true when passed true")
	}
}

func TestConfig_invalidInputFallsBackToDefault(t *testing.T) {
	c := newConfig(100, 100, 0, false)
	// Manually set invalid value in the text input
	c.fields[0].input.SetValue("abc")
	if c.Rows() != 100 {
		t.Errorf("invalid input should fallback to 100, got %d", c.Rows())
	}
}

func TestConfig_negativeValueFallsBack(t *testing.T) {
	c := newConfig(100, 100, 0, false)
	c.fields[0].input.SetValue("-5")
	if c.Rows() != 100 {
		t.Errorf("negative input should fallback to 100, got %d", c.Rows())
	}
}

func TestConfig_batchSizeZeroClampedToOne(t *testing.T) {
	c := newConfig(100, 100, 0, false)
	c.fields[1].input.SetValue("0")
	if c.BatchSize() != 1 {
		t.Errorf("BatchSize(0) should clamp to 1 to prevent infinite loop, got %d", c.BatchSize())
	}
}

func TestConfig_zeroIsValid(t *testing.T) {
	c := newConfig(100, 100, 0, false)
	c.fields[2].input.SetValue("0")
	if c.EnumRows() != 0 {
		t.Errorf("0 should be valid for enum-rows, got %d", c.EnumRows())
	}
}

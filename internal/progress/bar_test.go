package progress

import (
	"strings"
	"testing"
)

func TestBar_Counters(t *testing.T) {
	b := New(100)
	b.IncValid()
	b.IncValid()
	b.IncInvalid()
	b.IncError()
	b.IncHostNotFound()

	if v := b.valid.Load(); v != 2 {
		t.Errorf("valid=%d, want 2", v)
	}
	if v := b.invalid.Load(); v != 1 {
		t.Errorf("invalid=%d, want 1", v)
	}
	if v := b.errCount.Load(); v != 1 {
		t.Errorf("errCount=%d, want 1", v)
	}
	if v := b.hostNotFound.Load(); v != 1 {
		t.Errorf("hostNotFound=%d, want 1", v)
	}
	if v := b.processed.Load(); v != 5 {
		t.Errorf("processed=%d, want 5", v)
	}
}

func TestBar_Render_Format(t *testing.T) {
	b := New(200)
	for i := 0; i < 100; i++ {
		b.IncValid()
	}
	s := b.render(500)
	if !strings.Contains(s, "100/200") {
		t.Errorf("render missing progress count: %q", s)
	}
	if !strings.Contains(s, "50%") {
		t.Errorf("render missing percentage: %q", s)
	}
	if !strings.Contains(s, "valid: 100") {
		t.Errorf("render missing valid count: %q", s)
	}
	if !strings.Contains(s, "500 acc/s") {
		t.Errorf("render missing speed: %q", s)
	}
}

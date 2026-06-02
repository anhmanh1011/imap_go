package proxy

import (
	"os"
	"sync"
	"testing"
)

func TestPool_RoundRobin(t *testing.T) {
	p := New(KindHTTPConnect)
	p.SetProxies([]string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80"})
	want := []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80", "1.1.1.1:80", "2.2.2.2:80"}
	for i, w := range want {
		if got := p.Next(); got != w {
			t.Errorf("call %d: Next()=%q, want %q", i+1, got, w)
		}
	}
}

func TestPool_Empty(t *testing.T) {
	p := New(KindHTTPConnect)
	if got := p.Next(); got != "" {
		t.Errorf("empty pool: Next()=%q, want \"\"", got)
	}
}

func TestPool_SetProxies_AtomicSwap(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"a:1", "b:2"})
	if p.Len() != 2 {
		t.Fatalf("Len=%d after first SetProxies, want 2", p.Len())
	}
	p.Next()
	p.SetProxies([]string{"x:9", "y:8", "z:7"})
	if p.Len() != 3 {
		t.Fatalf("Len=%d after second SetProxies, want 3", p.Len())
	}
	for i := 0; i < 6; i++ {
		got := p.Next()
		if got != "x:9" && got != "y:8" && got != "z:7" {
			t.Errorf("Next()=%q not in new list", got)
		}
	}
}

func TestPool_SetProxies_TrimsBlanks(t *testing.T) {
	p := New(KindHTTPConnect)
	p.SetProxies([]string{"  1.2.3.4:80  ", "", "  ", "5.6.7.8:80"})
	if p.Len() != 2 {
		t.Errorf("Len=%d, want 2 (blanks should drop)", p.Len())
	}
	if got := p.Next(); got != "1.2.3.4:80" {
		t.Errorf("Next()=%q, want 1.2.3.4:80", got)
	}
}

func TestPool_Kind(t *testing.T) {
	if k := New(KindSOCKS5).Kind(); k != KindSOCKS5 {
		t.Errorf("Kind()=%d, want KindSOCKS5", k)
	}
	if k := New(KindHTTPConnect).Kind(); k != KindHTTPConnect {
		t.Errorf("Kind()=%d, want KindHTTPConnect", k)
	}
}

func TestPool_ConcurrentNext(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"a", "b", "c", "d", "e"})
	const goroutines = 32
	const perG = 1000
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				if got := p.Next(); got == "" {
					t.Errorf("unexpected empty Next() under load")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestPool_MarkFailed_Eviction(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"a:1", "b:2", "c:3"})

	// 2 fails on "a:1" — still below threshold (3)
	p.MarkFailed("a:1")
	p.MarkFailed("a:1")
	if p.Evicted() != 0 {
		t.Errorf("after 2 fails, Evicted=%d, want 0", p.Evicted())
	}

	// 3rd fail crosses the threshold
	p.MarkFailed("a:1")
	if p.Evicted() != 1 {
		t.Errorf("after 3 fails, Evicted=%d, want 1", p.Evicted())
	}
}

func TestPool_Next_SkipsEvicted(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"dead:1", "alive:2"})

	// Evict "dead:1"
	for i := 0; i < int(evictThreshold); i++ {
		p.MarkFailed("dead:1")
	}

	// Next should consistently return "alive:2" (the only non-evicted entry)
	for i := 0; i < 20; i++ {
		if got := p.Next(); got != "alive:2" {
			t.Errorf("call %d: Next()=%q, want alive:2", i, got)
		}
	}
}

func TestPool_Next_AllEvicted_GivesUp(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"a:1", "b:2", "c:3"})
	for _, addr := range []string{"a:1", "b:2", "c:3"} {
		for i := 0; i < int(evictThreshold); i++ {
			p.MarkFailed(addr)
		}
	}
	// All evicted — Next still returns SOMETHING from the list, not ""
	got := p.Next()
	if got != "a:1" && got != "b:2" && got != "c:3" {
		t.Errorf("all-evicted Next()=%q, want a/b/c", got)
	}
}

func TestPool_SetProxies_ResetsEviction(t *testing.T) {
	p := New(KindSOCKS5)
	p.SetProxies([]string{"x:1"})
	for i := 0; i < int(evictThreshold); i++ {
		p.MarkFailed("x:1")
	}
	if p.Evicted() != 1 {
		t.Fatalf("pre-reset Evicted=%d, want 1", p.Evicted())
	}
	// New list arrives — eviction state should be wiped.
	p.SetProxies([]string{"x:1", "y:2"})
	if p.Evicted() != 0 {
		t.Errorf("post-refresh Evicted=%d, want 0", p.Evicted())
	}
	// And Next should pick x:1 normally now.
	first := p.Next()
	if first != "x:1" && first != "y:2" {
		t.Errorf("post-refresh Next()=%q, want x or y", first)
	}
}

func TestPool_MarkFailed_Empty_NoOp(t *testing.T) {
	p := New(KindSOCKS5)
	p.MarkFailed("") // must not panic or insert empty key
	if p.Evicted() != 0 {
		t.Errorf("Evicted=%d, want 0", p.Evicted())
	}
}

func TestPool_ConcurrentMarkFailedAndNext(t *testing.T) {
	p := New(KindSOCKS5)
	const n = 100
	list := make([]string, n)
	for i := range list {
		list[i] = "p" + string(rune('A'+i%26)) + ":" + string(rune('0'+i%10))
	}
	p.SetProxies(list)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				p.Next()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				p.MarkFailed(list[j%n])
			}
		}()
	}
	wg.Wait()
}

func TestLoadFile_Entries(t *testing.T) {
	f, err := os.CreateTemp("", "proxies*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("10.0.0.1:8080\n10.0.0.2:8080\n\n  10.0.0.3:8080  \n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	p, err := LoadFile(f.Name())
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if p.Kind() != KindHTTPConnect {
		t.Errorf("LoadFile kind=%d, want KindHTTPConnect", p.Kind())
	}
	if p.Len() != 3 {
		t.Errorf("Len()=%d, want 3", p.Len())
	}
	if p.Next() != "10.0.0.1:8080" {
		t.Errorf("first proxy wrong: %q", p.Next())
	}
}

func TestLoadFile_EmptyPath(t *testing.T) {
	p, err := LoadFile("")
	if err != nil {
		t.Fatalf("LoadFile(\"\") error: %v", err)
	}
	if p.Len() != 0 {
		t.Errorf("Len()=%d, want 0", p.Len())
	}
	if p.Next() != "" {
		t.Errorf("Next()=%q, want \"\"", p.Next())
	}
}

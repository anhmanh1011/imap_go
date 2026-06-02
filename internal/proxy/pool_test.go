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

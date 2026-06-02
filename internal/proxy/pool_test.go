package proxy

import (
	"os"
	"testing"
)

func TestPool_RoundRobin(t *testing.T) {
	p := &Pool{proxies: []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80"}}
	want := []string{"1.1.1.1:80", "2.2.2.2:80", "3.3.3.3:80", "1.1.1.1:80", "2.2.2.2:80"}
	for i, w := range want {
		if got := p.Next(); got != w {
			t.Errorf("call %d: Next()=%q, want %q", i+1, got, w)
		}
	}
}

func TestPool_Empty(t *testing.T) {
	p := &Pool{}
	if got := p.Next(); got != "" {
		t.Errorf("empty pool: Next()=%q, want \"\"", got)
	}
}

func TestLoadFile_Entries(t *testing.T) {
	f, err := os.CreateTemp("", "proxies*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("10.0.0.1:8080\n10.0.0.2:8080\n\n  10.0.0.3:8080  \n")
	f.Close()

	p, err := LoadFile(f.Name())
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
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

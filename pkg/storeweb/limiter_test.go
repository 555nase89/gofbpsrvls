package storeweb

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for i := range 3 {
		if w := l.take("anna"); w != 0 {
			t.Fatalf("attempt %d: wait %s", i+1, w)
		}
		now = now.Add(10 * time.Second)
	}
	// The first attempt was 30 s ago, so it expires in 30 s.
	if w := l.take("anna"); w != 30*time.Second {
		t.Fatalf("4th attempt: wait %s", w)
	}
	if w := l.take("bert"); w != 0 {
		t.Fatalf("other name: wait %s", w)
	}
	now = now.Add(30 * time.Second)
	if w := l.take("anna"); w != 0 {
		t.Fatalf("after the first expired: wait %s", w)
	}
	if w := l.take("anna"); w != 10*time.Second {
		t.Fatalf("again: wait %s", w)
	}
	l.reset("anna")
	if w := l.take("anna"); w != 0 {
		t.Fatalf("after reset: wait %s", w)
	}
	// Names without recent attempts are swept.
	now = now.Add(2 * time.Minute)
	l.take("carl")
	if len(l.tries) != 1 {
		t.Fatalf("not swept: %v", l.tries)
	}
}

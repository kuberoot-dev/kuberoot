package rt

import "testing"

func TestStretches(t *testing.T) {
	s := newStretches(100)
	// A stretch anchored 3 ms early: its starts are 3000-3005 µs late, and
	// its last job, a stall, 3090.
	for _, us := range []int64{3000, 3002, 3005, 3001} {
		s.add(us, false)
	}
	s.add(3090, true)
	// A stretch anchored late: starts before their schedule.
	for _, us := range []int64{-20, -18, -20} {
		s.add(us, false)
	}
	s.close()
	if s.samples != 8 || s.max != 90 {
		t.Fatalf("samples %d max %d", s.samples, s.max)
	}
	want := map[int]int64{0: 3, 1: 1, 2: 2, 5: 1, 90: 1}
	var n int64
	for us, c := range s.hist {
		if c != want[us] {
			t.Errorf("hist[%d] = %d, want %d", us, c, want[us])
		}
		n += c
	}
	if n != 8 || s.sum != 0+2+5+1+90+0+2+0 {
		t.Errorf("hist holds %d, sum %d", n, s.sum)
	}
	// Past the histogram: counted, and only in the maximum.
	s.add(0, false)
	s.add(500, true)
	if s.samples != 10 || s.max != 500 || s.hist[0] != 4 {
		t.Errorf("samples %d max %d hist[0] %d", s.samples, s.max, s.hist[0])
	}
}

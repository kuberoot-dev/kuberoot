package rt

import "math"

// stretches turns a deadline probe's start times into latencies. Starts are
// measured per stretch of regular periods, in microseconds from a schedule
// anchored anywhere in it; a stretch's latencies are its starts less its
// earliest. Starts are kept around the stretch's first, as far before and
// after it as the histogram reaches. Memory is allocated once: adding runs
// between the probe's jobs.
type stretches struct {
	base    int64
	starts  []int64
	hist    []int64
	minUs   int64
	maxUs   int64
	sumUs   int64
	n       int64
	samples int64
	sum     int64
	max     int64
}

func newStretches(histUs int) *stretches {
	return &stretches{starts: make([]int64, 2*histUs), hist: make([]int64, histUs),
		minUs: math.MaxInt64, maxUs: math.MinInt64}
}

func (s *stretches) index(us int64) int64 { return us - s.base + int64(len(s.hist)) }

// add counts a start; ends says the job ended its stretch.
func (s *stretches) add(us int64, ends bool) {
	if s.n == 0 {
		s.base = us
	}
	if i := s.index(us); i >= 0 && i < int64(len(s.starts)) {
		s.starts[i]++
	}
	s.minUs, s.maxUs, s.sumUs, s.n = min(s.minUs, us), max(s.maxUs, us), s.sumUs+us, s.n+1
	if ends {
		s.close()
	}
}

// close ends the stretch: its latencies go into the histogram, those past
// its end only into the maximum.
func (s *stretches) close() {
	if s.n == 0 {
		return
	}
	for i := max(s.index(s.minUs), 0); i <= min(s.index(s.maxUs), int64(len(s.starts))-1); i++ {
		if n := s.starts[i]; n > 0 {
			if us := i - s.index(s.minUs); us < int64(len(s.hist)) {
				s.hist[us] += n
			}
			s.starts[i] = 0
		}
	}
	s.samples += s.n
	s.sum += s.sumUs - s.n*s.minUs
	s.max = max(s.max, s.maxUs-s.minUs)
	s.minUs, s.maxUs, s.sumUs, s.n = math.MaxInt64, math.MinInt64, 0, 0
}

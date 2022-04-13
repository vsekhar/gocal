package interval

import (
	"log"
	"sort"
	"sync"
	"time"
)

type Interval struct {
	Start, End time.Time
}

func (i Interval) Less(j Interval) bool {
	if i.Start.Before(j.Start) {
		return true
	}
	if i.Start.Equal(j.Start) && i.End.Before(j.End) {
		return true
	}
	return false
}

func (i Interval) Overlaps(j Interval) bool {
	if j.Start.Before(i.End) && i.Start.Before(j.End) {
		return true
	}
	return false
}

func OrDie(s, e string) Interval {
	return Interval{
		Start: dateTimeOrDie(s),
		End:   dateTimeOrDie(e),
	}
}

func dateTimeOrDie(s string) time.Time {
	if x, err := time.Parse(time.RFC3339, s); err != nil {
		log.Fatalf("'%s' cannot be converted to time: %v", s, err)
	} else {
		return x
	}
	panic("unreachable") // suppress compiler error
}

type Map[T any] struct {
	intervals []Interval
	data      []T
}

func (im *Map[T]) Add(start, end time.Time, t T) {
	itr := Interval{start, end}
	i := sort.Search(len(im.intervals), func(i int) bool {
		return itr.Less(im.intervals[i])
	})
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		im.intervals = append(im.intervals, Interval{})
		copy(im.intervals[i+1:], im.intervals[i:])
		im.intervals[i] = itr
	}()
	go func() {
		defer wg.Done()
		var zero T
		im.data = append(im.data, zero)
		copy(im.data[i+1:], im.data[i:])
		im.data[i] = t
	}()
	wg.Wait()
}

// Covering returns all values whose intervals cover [start and end].
func (im *Map[T]) Covering(start, end time.Time) []T {
	okFunc := func(i int) bool {
		if !start.Before(im.intervals[i].Start) && !im.intervals[i].End.Before(end) {
			return true
		}
		return false
	}
	i := sort.Search(len(im.intervals), okFunc)
	if i == len(im.intervals) {
		return nil
	}
	ret := make([]T, 0)
	for ; i < len(im.intervals); i++ {
		if !okFunc(i) {
			break
		}
		ret = append(ret, im.data[i])
	}
	return ret
}

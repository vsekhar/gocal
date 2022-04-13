package batch_test

import (
	"sync"
	"testing"

	"github.com/vsekhar/gocal/internal/batch"
)

func TestBatch(t *testing.T) {
	v := make(chan int, 10)
	b := make(chan []int)

	wg := sync.WaitGroup{}
	wg.Add(2)

	// Producer
	go func() {
		defer wg.Done()
		defer close(v)
		for i := 0; i < 100; i++ {
			v <- i
		}
	}()

	// Consumer
	biggestBatch := 0
	go func() {
		defer wg.Done()
		for b := range b {
			t.Logf("batch: %v", b)
			if len(b) > biggestBatch {
				biggestBatch = len(b)
			}
		}
	}()
	batch.Up(v, b)
	close(b)
	wg.Wait()
	if biggestBatch <= 1 {
		t.Errorf("expected batches with multiple values, got largest batch size %d", biggestBatch)
	}
}

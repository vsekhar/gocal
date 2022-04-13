package batch

// Up batches values into batches. Up attempts to read from values and create a
// batch until all values are consumed. The batch is then submitted to batches.
//
// Up preserves the order of values; the first value received from the values
// channel will be the first value in the first batch sent on the batches channel,
// and so on.
//
// The values channel is typically buffered. The batches channel may be buffered.
// Up will wait until the send on batches completes before reading from values and
// creating a new batch.
//
// Up will terminate when values is closed, all values have been received, and all
// batches have been sent. If batches is unbuffered, the termination of Up happens
// after the last batch has been sent.
//
// Up does not itself close any channels. Typically the caller will need to close
// batches in order to terminate any consuming goroutine.
func Up[T any](values <-chan T, batches chan<- []T) {
	for {
		var batch []T
		var v T
		var ok bool
		// gather up a batch via non-blocking receives
	batch:
		for {
			select {
			case v, ok = <-values:
				if !ok {
					if len(batch) > 0 {
						batches <- batch
					}
					return
				}
				batch = append(batch, v)
				continue batch
			default:
				if len(batch) > 0 {
					break batch
				}
				// try blocking receive
				v, ok = <-values
				if !ok {
					return
				}
				batch = append(batch, v)
				continue batch
			}
		}
		batches <- batch
	}
}

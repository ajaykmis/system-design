package datawriter

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// multiple producer and single consumer pattern to write data to a file

type MPSCWriter struct {
	queue chan []byte // queue channel multiple producers and single consumer.
	file  *os.File    // append only file to write data.
	wg    sync.WaitGroup
	// shutdown chan bool // Add this
}

func NewMPSCWriter(file *os.File, queuesize int) *MPSCWriter {
	writer := &MPSCWriter{
		queue: make(chan []byte, queuesize),
		file:  file,
		// shutdown: make(chan bool), // Initialize the shutdown channel
	}
	go writer.backgroundWriter()
	writer.wg.Add(1)
	return writer
}

func (w *MPSCWriter) Close() error {
	// if w.shutdown != nil {
	// 	close(w.shutdown) // Signal shutdown
	// 	w.wg.Wait()       // Wait for drain to complete
	// }
	close(w.queue) // Signal no more data coming
	w.wg.Wait()    // Wait for background writer to finish
	return nil
}

func (w *MPSCWriter) backgroundWriter() {
	// ms ticker
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	batch := make([][]byte, 0, cap(w.queue)) // slice of byte slices
	defer w.wg.Done()
	for {
		select {
		case data, ok := <-w.queue:
			if !ok {
				// Channel closed - drain and exit
				if len(batch) > 0 {
					w.writeBatch(batch)
				}
				return
			}
			batch = append(batch, data)
		// collect data from the queue channel
		case <-ticker.C:
			fmt.Print("Collecting data from queue... ")
			for len(batch) < 5 { // write in batches of 100
				fmt.Println("writing to batch...", len(batch), batch)
				select {
				case data := <-w.queue:
					batch = append(batch, data)
				default:
					fmt.Println("No more data in queue, writing batch if any...")
					goto writeBatch
				}
			}
		writeBatch:
			if len(batch) > 0 {
				w.writeBatch(batch)
				batch = batch[:0] // reset batch
			}
		}
	}
}

func (w *MPSCWriter) writeBatch(batch [][]byte) {
	fmt.Printf("Writing batch of %d items to file.\n", len(batch))
	for _, data := range batch {
		w.file.Write(data)
	}
	w.file.Sync() // ensure data is flushed to disk
	fmt.Printf("Wrote batch of %d items to file.\n", len(batch))
}

var ErrQueueFull = errors.New("Queue is full, backpressure applied")

func (w *MPSCWriter) Push(data []byte) error {

	// where's the queue size limit handling?
	select {
	case w.queue <- data: // send data to the queue channel, natuarally blocks if the channel is full
		// successfully sent
		fmt.Println("Data pushed to queue successfully.")
		return nil
	default:
		// handle queue full backpressure
		// queue is full, block until there's space
		fmt.Println("Queue full, blocking until space is available...")
		return ErrQueueFull
	}

}

func (w *MPSCWriter) drainQueue() {
	fmt.Println("Draining remaining items from queue...")

	// Process all remaining items in the queue
	batch := make([][]byte, 0)

drainLoop:
	for {
		select {
		case data := <-w.queue:
			batch = append(batch, data)

			// Write in batches to avoid huge memory usage
			if len(batch) >= 100 {
				w.writeBatch(batch)
				batch = batch[:0] // Reset batch
			}
		default:
			// No more items in queue
			break drainLoop
		}
	}

	// Write final batch if any items remain
	if len(batch) > 0 {
		w.writeBatch(batch)
	}

	fmt.Printf("Finished draining queue. Processed all remaining items.\n")
}

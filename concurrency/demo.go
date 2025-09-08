package main

// solve exercises in concurrency using goroutines and channels
// https://github.com/loong/go-concurrency-exercises/tree/main/1-producer-consumer
import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

const (
	numPublishers = 3
	numConsumers  = 5
	numMessages   = 10
)

func main() {
	rand.Seed(time.Now().UnixNano())

	messageChannel := make(chan int, 10)
	var wg sync.WaitGroup

	// Publishers
	for i := 1; i <= numPublishers; i++ {
		wg.Add(1)
		go func(publisherID int) {
			defer wg.Done()
			for j := 0; j < numMessages; j++ {
				message := rand.Intn(100)
				fmt.Printf("Publisher %d: Publishing message %d\n", publisherID, message)
				messageChannel <- message
				time.Sleep(time.Millisecond * time.Duration(rand.Intn(500)))
			}
		}(i)
	}

	// Consumers
	for i := 1; i <= numConsumers; i++ {
		wg.Add(1)
		go func(consumerID int) {
			defer wg.Done()
			for message := range messageChannel {
				fmt.Printf("Consumer %d: Consumed message %d\n", consumerID, message)
				time.Sleep(time.Millisecond * time.Duration(rand.Intn(500)))
			}
		}(i)
	}

	// Close the channel once all publishers are done
	go func() {
		wg.Wait()
		close(messageChannel)
	}()

	// Wait for all consumers to finish
	wg.Wait()
	fmt.Println("All publishers and consumers are done.")
}

package main

// solve exercises in concurrency using goroutines and channels
// https://github.com/loong/go-concurrency-exercises/tree/main/1-producer-consumer
import (
	"fmt"
	"os"
	"sync"

	"system-design/concurrency/datawriter"
)

const (
	numPublishers = 2
	numConsumers  = 5
	numMessages   = 10
)

func main() {
	// plan:
	// 1. create base data writer,
	// 2. create multiple thtreads to push the data to the file
	// 3. created data writer to the file
	file, err := os.Create("data.txt")
	if err != nil {
		fmt.Println("Error creating file:", err)
		return
	}

	file2, err := os.Create("data2.txt")
	if err != nil {
		fmt.Println("Error creating file:", err)
		return
	}
	defer file.Close()
	defer file2.Close()
	// create a writer instance
	dw := datawriter.NewBaseDataWriter(file, 100)
	mpscWriter := datawriter.NewMPSCWriter(file2, 100)

	// create two slices of data to be written
	data1 := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	data2 := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"}

	// create wait group to wait for all goroutines to finish
	var wg sync.WaitGroup

	// start multiple goroutines to write data concurrently
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, d := range data1 {
			dw.Push([]byte(d + ","))
			mpscWriter.Push([]byte(d + ","))
		}
	}()

	go func() {
		defer wg.Done()
		for _, d := range data2 {
			dw.Push([]byte(d + ","))
			mpscWriter.Push([]byte(d + ","))
		}
	}()

	// wait for all goroutines to finish
	wg.Wait()
	fmt.Println("Data writing completed.")
	mpscWriter.Close() // Trigger shutdown and drain queue

	// demonstrate multiple producers and single consumer
	// multipleProducersSingleConsumer()
}

// func multipleProducersSingleConsumer() {
// 	rand.Seed(time.Now().UnixNano())

// 	messageChannel := make(chan int, 10)
// 	var wg sync.WaitGroup

// 	// Publishers
// 	for i := 1; i <= numPublishers; i++ {
// 		wg.Add(1)
// 		go func(publisherID int) {
// 			defer wg.Done()
// 			for j := 0; j < numMessages; j++ {
// 				message := rand.Intn(100)
// 				fmt.Printf("Publisher %d: Publishing message %d\n", publisherID, message)
// 				messageChannel <- message
// 				time.Sleep(time.Millisecond * time.Duration(rand.Intn(500)))
// 			}
// 		}(i)
// 	}

// 	// Consumers
// 	for i := 1; i <= numConsumers; i++ {
// 		wg.Add(1)
// 		go func(consumerID int) {
// 			defer wg.Done()
// 			for message := range messageChannel {
// 				fmt.Printf("Consumer %d: Consumed message %d\n", consumerID, message)
// 				time.Sleep(time.Millisecond * time.Duration(rand.Intn(500)))
// 			}
// 		}(i)
// 	}

// 	// Close the channel once all publishers are done
// 	go func() {
// 		wg.Wait()
// 		close(messageChannel)
// 	}()

// 	// Wait for all consumers to finish
// 	wg.Wait()
// 	fmt.Println("All publishers and consumers are done.")
// }

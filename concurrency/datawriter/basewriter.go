package datawriter

import (
	"os"
	"sync"
)

type BaseDataWriter struct {
	mutex sync.Mutex // Mutex to protect shared queue.
	file  *os.File   // append only file to write data.
}

func NewBaseDataWriter(file *os.File, queueSize int) *BaseDataWriter {
	return &BaseDataWriter{
		file: file,
	}
}

func (w *BaseDataWriter) Push(data []byte) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.file.Write(data)
}

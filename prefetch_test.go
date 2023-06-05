package main

import (
	"os"
	"runtime"
	"testing"
	"time"

	pond "github.com/alitto/pond"
	pq "github.com/emirpasic/gods/queues/priorityqueue"

	"github.com/stretchr/testify/assert"
)

func LazyModeSetup(t *testing.T) func() {
	// Setup
	config.LazyMode = true
	WorkQueue = pq.NewWith(byPriority) // empty

	// Create a buffered (non-blocking) pool that can scale up to runtime.NumCPU() workers
	// and has a buffer capacity of 1000 tasks
	WorkerPool = pond.New(runtime.NumCPU(), 1000)

	return func() {
		// Tear down
		config.LazyMode = false
		WorkerPool.StopAndWaitFor(15*time.Second)
    }
}

func TestPrefetchImages(t *testing.T) {
	fp := "./prefetch"
	_ = os.Mkdir(fp, 0755)
	prefetchImages("./pics/dir1/", "./prefetch")
	count := fileCount("./prefetch")
	assert.Equal(t, int64(1), count)
	_ = os.RemoveAll(fp)
}

func TestPrefetchImagesLazy(t *testing.T) {
	ts := LazyModeSetup(t)
	t.Cleanup(ts)

	fp := "./prefetch"
	_ = os.Mkdir(fp, 0755)
	prefetchImages("./pics/dir1/", "./prefetch")
	WorkerPool.StopAndWait()
	count := fileCount("./prefetch")
	assert.Equal(t, int64(1), count)
	_ = os.RemoveAll(fp)
}

func TestBadPrefetch(t *testing.T) {
	jobs = 1
	prefetchImages("./pics2", "./prefetch")
}

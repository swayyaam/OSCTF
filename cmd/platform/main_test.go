package main

import (
	"sync"
	"testing"
	"time"
)

func TestWaitBoundedCompletesBeforeDeadline(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		wg.Done()
	}()
	if !waitBounded(&wg, time.Second) {
		t.Fatal("waitBounded reported timeout, but the worker finished well within the bound")
	}
}

func TestWaitBoundedTimesOutOnWedgedWorker(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1) // never Done: simulates a worker that will not return
	start := time.Now()
	if waitBounded(&wg, 30*time.Millisecond) {
		t.Fatal("waitBounded reported success, but the worker never finished")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("returned after %v, want to have waited the full ~30ms bound", elapsed)
	}
	wg.Done() // let the internal waiter goroutine exit cleanly
}

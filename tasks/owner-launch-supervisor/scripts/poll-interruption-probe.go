package main

import (
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
)

// Task-scoped diagnostic only: exact empty pipe/socket shape, no payload read.
func main() {
	runtime.LockOSThread()
	reader, writer, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	defer writer.Close()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(pair[0])
	defer syscall.Close(pair[1])
	received := make(chan os.Signal, 64)
	signal.Notify(received, syscall.SIGUSR1)
	defer signal.Stop(received)
	stop := make(chan struct{})
	joined := make(chan struct{})
	var signals atomic.Int64
	go func() {
		defer close(joined)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGUSR1)
			signals.Add(1)
			time.Sleep(10 * time.Microsecond)
		}
	}()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-received:
			case <-stop:
				return
			}
		}
	}()
	counts := map[string]int{}
	start := time.Now()
	iterations := 0
	for iterations < 500000 && time.Since(start) < 5*time.Second {
		fds := []unix.PollFd{{Fd: int32(reader.Fd()), Events: unix.POLLIN}, {Fd: int32(pair[1]), Events: unix.POLLIN}}
		n, e := unix.Poll(fds, 0)
		iterations++
		if errors.Is(e, syscall.EINTR) {
			counts["EINTR"]++
			break
		}
		if e != nil {
			counts[e.Error()]++
			break
		}
		if n != 0 || fds[0].Revents != 0 || fds[1].Revents != 0 {
			counts["unexpected_readiness"]++
			break
		}
	}
	close(stop)
	<-joined
	<-drained
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "iterations": iterations, "signals": signals.Load(), "seconds": time.Since(start).Seconds(), "errors": counts})
}

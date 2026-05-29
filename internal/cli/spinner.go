package cli

import (
	"fmt"
	"sync"
	"time"
)

// Spinner prints an animated "... / ... / ..." indicator on a single line.
// Call Stop() to clear it before printing regular output.
type Spinner struct {
	msg    string
	done   chan struct{}
	wg     sync.WaitGroup
}

var spinnerFrames = []string{"   ", ".  ", ".. ", "..."}

// NewSpinner starts a spinner with the given message and returns it.
// The caller must call Stop() when the operation completes.
func NewSpinner(msg string) *Spinner {
	s := &Spinner{
		msg:  msg,
		done: make(chan struct{}),
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		i := 0
		for {
			select {
			case <-s.done:
				// Clear the line
				fmt.Printf("\r\033[K")
				return
			case <-time.After(400 * time.Millisecond):
				frame := spinnerFrames[i%len(spinnerFrames)]
				fmt.Printf("\r  %s %s", frame, msg)
				i++
			}
		}
	}()
	return s
}

// Stop halts the spinner and clears the line so normal output can follow.
func (s *Spinner) Stop() {
	close(s.done)
	s.wg.Wait()
}

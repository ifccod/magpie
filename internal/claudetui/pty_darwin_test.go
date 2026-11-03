//go:build darwin

package claudetui

// On macOS a new /dev/ptmx master refuses fcntl(F_SETFL) with ENOTTY until
// its slave has been opened once — 14.8.5 (x86_64) and 14.8.9 (arm64) both
// do — but the kernel applies O_NONBLOCK anyway. os.OpenFile asks for it
// while no slave exists yet, takes the error for "not pollable", and leaves
// the fd non-blocking and out of the runtime poller: every Read returns
// EAGAIN at once, so the shim reads an empty screen and gives up 45s later
// with "Claude Code showed no prompt" (#4834). Opened with O_NONBLOCK and
// wrapped by os.NewFile, the fd is in the poller from the start: reads wait
// for Claude Code, and a Close still ends them.

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestOpenPTYReadsWhatItsSlaveWrites is the run the interactive mode makes:
// a child draws on the slave, and the master is read.
func TestOpenPTYReadsWhatItsSlaveWrites(t *testing.T) {
	m, name, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	const want = 4000
	cmd := exec.Command("/bin/sh", "-c", "printf '%04000d' 0")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()

	type read struct {
		n   int
		err error
	}
	done := make(chan read, 1)
	go func() {
		buf := make([]byte, 64<<10)
		n := 0
		for n < want {
			k, err := m.Read(buf)
			n += k
			if err != nil {
				done <- read{n, err}
				return
			}
		}
		done <- read{n, nil}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("read %d of %d bytes: %v", r.n, want, r.err)
		}
		if r.n != want {
			t.Fatalf("read %d bytes, want %d", r.n, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came off the master in 5s")
	}
}

// TestOpenPTYCloseEndsARead keeps the fix from being a blocking descriptor:
// the shim reads until its Claude Code stops, and a read that outlives it
// must end with the terminal's Close.
func TestOpenPTYCloseEndsARead(t *testing.T) {
	m, _, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, err := m.Read(buf)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	m.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the read ended without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read is still blocked 5s after the master was closed")
	}
}

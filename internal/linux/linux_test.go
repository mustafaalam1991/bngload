package linux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func TestIfreqSize(t *testing.T) {
	if s := unsafe.Sizeof(ifreqData{}); s != 40 {
		t.Fatalf("struct ifreq size %d, want 40", s)
	}
}

func TestEffectiveCapsParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status")
	// bits 12 (NET_ADMIN) and 13 (NET_RAW) set
	_ = os.WriteFile(p, []byte("Name:\tx\nCapEff:\t0000000000003000\n"), 0o644)
	caps, err := effectiveCapsFrom(p)
	if err != nil {
		t.Fatal(err)
	}
	if !caps["CAP_NET_ADMIN"] || !caps["CAP_NET_RAW"] || caps["CAP_SYS_ADMIN"] {
		t.Fatalf("caps %v", caps)
	}
}

func TestClockGettimeMonotonic(t *testing.T) {
	a, err := ClockGettime(ClockMonotonicRaw)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	b, _ := ClockGettime(ClockMonotonicRaw)
	if b-a < int64(time.Millisecond) {
		t.Fatalf("clock did not advance: %d", b-a)
	}
}

func TestPollTimeout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fds := []PollFd{{Fd: int32(r.Fd()), Events: POLLIN}}
	if n, err := Poll(fds, 10*time.Millisecond); err != nil || n != 0 {
		t.Fatalf("empty pipe: n=%d err=%v", n, err)
	}
	_, _ = w.Write([]byte("x"))
	if n, err := Poll(fds, time.Second); err != nil || n != 1 || fds[0].Revents&POLLIN == 0 {
		t.Fatalf("readable pipe: n=%d err=%v revents=%x", n, err, fds[0].Revents)
	}
}

func TestHtonsAndClockID(t *testing.T) {
	if Htons(0x88B5) != 0xB588 {
		t.Fatal("htons")
	}
	// FD_TO_CLOCKID(3) = (~3 << 3) | 3 = 0xFFFFFFE3 as int32 = -29
	if int32(FDToClockID(3)) != -29 {
		t.Fatalf("FDToClockID(3) = %d", int32(FDToClockID(3)))
	}
}

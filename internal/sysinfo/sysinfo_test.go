package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKernelAtLeast(t *testing.T) {
	cases := []struct {
		rel  string
		ma   int
		mi   int
		want bool
	}{
		{"6.8.0-45-generic", 5, 15, true},
		{"5.15.0-119-generic", 5, 15, true},
		{"5.4.0-1-generic", 5, 15, false},
		{"6.8.0", 6, 8, true},
		{"6.10rc1", 6, 8, true},
		{"garbage", 1, 0, false},
	}
	for _, c := range cases {
		if got := KernelAtLeast(c.rel, c.ma, c.mi); got != c.want {
			t.Errorf("KernelAtLeast(%q,%d,%d)=%v", c.rel, c.ma, c.mi, got)
		}
	}
}

func TestFixtureTree(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(s), 0o644)
	}
	write("etc/os-release", "ID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n")
	write("proc/sys/net/core/rmem_max", "67108864\n")
	write("proc/meminfo", "MemTotal:       65536000 kB\nHugePages_Total:       4\nHugepagesize:    1048576 kB\n")
	write("sys/devices/system/cpu/isolated", "2-9\n")
	write("sys/devices/system/cpu/nohz_full", "(null)\n")
	write("proc/123/comm", "chronyd\n")
	old := Root
	Root = root
	defer func() { Root = old }()

	if OSRelease()["PRETTY_NAME"] != "Ubuntu 24.04.1 LTS" {
		t.Fatal("os-release")
	}
	if SysctlInt("net.core.rmem_max") != 67108864 || SysctlInt("net.core.absent") != -1 {
		t.Fatal("sysctl")
	}
	m := MemoryInfo()
	if m.TotalKiB != 65536000 || m.HugePages != 4 || m.HugePageKiB != 1048576 {
		t.Fatalf("meminfo %+v", m)
	}
	c := CPUInfo()
	if c.Isolated != "2-9" || c.NohzFull != "" {
		t.Fatalf("cpu %+v", c)
	}
	if !RunningProcesses()["chronyd"] {
		t.Fatal("processes")
	}
}

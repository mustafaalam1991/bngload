package netlinkx

import (
	"encoding/binary"
	"testing"
)

func TestAttrEncodingAlignsAndNests(t *testing.T) {
	a := attr{typ: 18, children: []attr{
		{typ: 1, data: []byte("veth")},            // 4+4 = 8
		{typ: 2, children: []attr{u32Attr(1, 7)}}, // 4 + (4+4) = 12
	}}
	b := a.encode()
	if len(b) != 4+8+12 {
		t.Fatalf("encoded length %d", len(b))
	}
	if binary.NativeEndian.Uint16(b[0:]) != uint16(len(b)) || binary.NativeEndian.Uint16(b[2:]) != 18 {
		t.Fatal("outer header")
	}
	s := strAttr(3, "abc") // "abc\0" = 4 bytes + 4 header = 8, already aligned
	if e := s.encode(); len(e) != 8 || e[7] != 0 {
		t.Fatalf("string attr %v", e)
	}
	odd := attr{typ: 9, data: []byte{1}} // 5 bytes → padded to 8, length field 5
	if e := odd.encode(); len(e) != 8 || binary.NativeEndian.Uint16(e) != 5 {
		t.Fatalf("padding %v", e)
	}
}

func TestIfinfomsgLayout(t *testing.T) {
	b := ifinfomsg(10, 42, 1, 1)
	if len(b) != 16 || b[0] != 10 || binary.NativeEndian.Uint32(b[4:]) != 42 ||
		binary.NativeEndian.Uint32(b[8:]) != 1 || binary.NativeEndian.Uint32(b[12:]) != 1 {
		t.Fatalf("ifinfomsg %v", b)
	}
}

func TestLinksAndLookupOnLoopback(t *testing.T) {
	c, err := Dial()
	if err != nil {
		t.Skip("netlink unavailable:", err)
	}
	defer c.Close()
	links, err := c.Links()
	if err != nil {
		t.Fatal(err)
	}
	var lo int
	for _, l := range links {
		if l.Name == "lo" {
			lo = l.Index
		}
	}
	if lo == 0 {
		t.Fatal("lo not listed")
	}
	idx, err := c.LinkIndex("lo")
	if err != nil || idx != lo {
		t.Fatalf("LinkIndex(lo) = %d, %v; want %d", idx, err, lo)
	}
	if _, err := c.LinkIndex("bkb-does-not-exist"); err == nil {
		t.Fatal("expected not-exist error")
	}
}

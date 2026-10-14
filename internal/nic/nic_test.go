package nic

import (
	"testing"
	"unsafe"
)

// The ioctl structures must match the kernel ABI byte for byte.
func TestStructSizes(t *testing.T) {
	if s := unsafe.Sizeof(ethtoolDrvinfo{}); s != 196 {
		t.Fatalf("ethtool_drvinfo size %d, want 196", s)
	}
	if s := unsafe.Sizeof(ethtoolTsInfo{}); s != 44 {
		t.Fatalf("ethtool_ts_info size %d, want 44", s)
	}
	if s := unsafe.Sizeof(HwTstampConfig{}); s != 12 {
		t.Fatalf("hwtstamp_config size %d, want 12", s)
	}
}

func TestDecodeTsInfo(t *testing.T) {
	r := ethtoolTsInfo{
		SoTimestamping: TsTxHardware | TsTxSoftware | TsRxHardware | TsRxSoftware | TsSoftware | TsRawHardware,
		PhcIndex:       2,
		TxTypes:        1<<HwTxOff | 1<<HwTxOn,
		RxFilters:      1<<HwFilterNone | 1<<HwFilterAll,
	}
	ti := decodeTsInfo(r)
	if !ti.HasSoftware() || !ti.HasHardware() || !ti.HwFilterAllSupported() || ti.PHCIndex != 2 {
		t.Fatalf("unexpected %+v", ti)
	}
	if len(ti.TxTypes) != 2 || ti.RxFilters[1] != "all" {
		t.Fatalf("names %+v %+v", ti.TxTypes, ti.RxFilters)
	}
	// PTP-only NIC: hardware capable, but cannot stamp PPPoE frames.
	r.RxFilters = 1<<HwFilterNone | 1<<12 // ptpv2-event
	r.PhcIndex = -1
	ti = decodeTsInfo(r)
	if ti.HwFilterAllSupported() || ti.HasHardware() {
		t.Fatalf("PTP-only / no-PHC NIC misclassified: %+v", ti)
	}
	if ti.RxFilters[1] != "ptpv2-event" {
		t.Fatalf("filter name %v", ti.RxFilters)
	}
}

func TestLinkFactsLoopback(t *testing.T) {
	l, err := LinkFacts("lo")
	if err != nil {
		t.Skip("no loopback in this environment:", err)
	}
	if l.Index <= 0 || !l.Virtual {
		t.Fatalf("unexpected loopback facts %+v", l)
	}
	if len(l.IPv4) > 0 && !l.HasRoutableAddress() {
		t.Fatal("HasRoutableAddress inconsistent")
	}
}

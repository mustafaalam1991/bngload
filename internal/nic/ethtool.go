// Package nic reads network-interface facts that matter for bngload:
// driver and firmware identity, timestamping capabilities, offload features,
// interrupt coalescing, ring sizes, queue counts, sysfs attributes and
// configured addresses.
//
// It talks to drivers through the classic SIOCETHTOOL ioctl (the same API
// ethtool(8) uses), so no external tools are required. All functions are
// read-only except SetHwTstamp, which the timestamp self-test uses and
// always reverts.
package nic

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/report"
)

// ethtool command numbers (from <linux/ethtool.h>).
const (
	ethtoolGDrvInfo   = 0x00000003
	ethtoolGCoalesce  = 0x0000000e
	ethtoolGRingParam = 0x00000010
	ethtoolGStrings   = 0x0000001b
	ethtoolGSsetInfo  = 0x00000037
	ethtoolGFeatures  = 0x0000003a
	ethtoolGChannels  = 0x0000003c
	ethtoolGetTsInfo  = 0x00000041

	ethSSFeatures = 4 // ETH_SS_FEATURES string set
	ethGStringLen = 32
)

// DriverInfo is the identity reported by ETHTOOL_GDRVINFO.
type DriverInfo struct {
	Driver     string `json:"driver"`
	Version    string `json:"version"`
	Firmware   string `json:"firmware"`
	BusInfo    string `json:"bus_info"`
	ROMVersion string `json:"rom_version,omitempty"`
}

// ethtoolDrvinfo mirrors struct ethtool_drvinfo.
type ethtoolDrvinfo struct {
	Cmd         uint32
	Driver      [32]byte
	Version     [32]byte
	FwVersion   [32]byte
	BusInfo     [32]byte
	EromVersion [32]byte
	Reserved2   [12]byte
	NPrivFlags  uint32
	NStats      uint32
	TestinfoLen uint32
	EedumpLen   uint32
	RegdumpLen  uint32
}

// Driver returns driver, firmware and bus identity for ifname.
func Driver(ifname string) (DriverInfo, error) {
	d := ethtoolDrvinfo{Cmd: ethtoolGDrvInfo}
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&d)); err != nil {
		return DriverInfo{}, fmt.Errorf("ETHTOOL_GDRVINFO %s: %w", ifname, err)
	}
	return DriverInfo{
		Driver:     report.CString(d.Driver[:]),
		Version:    report.CString(d.Version[:]),
		Firmware:   report.CString(d.FwVersion[:]),
		BusInfo:    report.CString(d.BusInfo[:]),
		ROMVersion: report.CString(d.EromVersion[:]),
	}, nil
}

// ---------------------------------------------------------------------------
// Timestamping capabilities
// ---------------------------------------------------------------------------

// SO_TIMESTAMPING capability and request flags (from <linux/net_tstamp.h>).
const (
	TsTxHardware  = 1 << 0
	TsTxSoftware  = 1 << 1
	TsRxHardware  = 1 << 2
	TsRxSoftware  = 1 << 3
	TsSoftware    = 1 << 4
	TsSysHardware = 1 << 5 // deprecated
	TsRawHardware = 1 << 6
	TsOptID       = 1 << 7
	TsTxSched     = 1 << 8
	TsTxAck       = 1 << 9
	TsOptCmsg     = 1 << 10
	TsOptTsOnly   = 1 << 11
)

var tsFlagNames = []struct {
	bit  uint32
	name string
}{
	{TsTxHardware, "hardware-transmit"},
	{TsTxSoftware, "software-transmit"},
	{TsRxHardware, "hardware-receive"},
	{TsRxSoftware, "software-receive"},
	{TsSoftware, "software-system-clock"},
	{TsSysHardware, "hardware-legacy-clock"},
	{TsRawHardware, "hardware-raw-clock"},
}

// HW timestamp TX types and RX filters (from <linux/net_tstamp.h>).
const (
	HwTxOff = 0
	HwTxOn  = 1

	HwFilterNone = 0
	HwFilterAll  = 1
)

var rxFilterNames = []string{
	"none", "all", "some",
	"ptpv1-l4-event", "ptpv1-l4-sync", "ptpv1-l4-delay-req",
	"ptpv2-l4-event", "ptpv2-l4-sync", "ptpv2-l4-delay-req",
	"ptpv2-l2-event", "ptpv2-l2-sync", "ptpv2-l2-delay-req",
	"ptpv2-event", "ptpv2-sync", "ptpv2-delay-req",
	"ntp-all",
}

var txTypeNames = []string{"off", "on", "onestep-sync", "onestep-p2p"}

// TsInfo is the decoded result of ETHTOOL_GET_TS_INFO.
type TsInfo struct {
	// Capabilities lists SO_TIMESTAMPING capabilities (ethtool -T wording).
	Capabilities []string `json:"capabilities"`
	// PHCIndex is the PTP hardware clock (/dev/ptpN), or -1 if none.
	PHCIndex  int      `json:"phc_index"`
	TxTypes   []string `json:"tx_types"`
	RxFilters []string `json:"rx_filters"`
	// Raw bit masks, kept for programmatic checks.
	SoTimestamping uint32 `json:"so_timestamping_mask"`
	TxTypesMask    uint32 `json:"tx_types_mask"`
	RxFiltersMask  uint32 `json:"rx_filters_mask"`
}

// HasSoftware reports full software RX+TX stamping support.
func (t TsInfo) HasSoftware() bool {
	need := uint32(TsRxSoftware | TsTxSoftware | TsSoftware)
	return t.SoTimestamping&need == need
}

// HasHardware reports raw hardware RX+TX stamping with a PHC.
func (t TsInfo) HasHardware() bool {
	need := uint32(TsRxHardware | TsTxHardware | TsRawHardware)
	return t.SoTimestamping&need == need && t.PHCIndex >= 0
}

// HwFilterAllSupported reports whether the NIC can hardware-stamp every
// received frame. Many NICs only stamp PTP frames; on those, PPPoE RX
// timestamps fall back to software (an important Phase 0 finding).
func (t TsInfo) HwFilterAllSupported() bool {
	return t.RxFiltersMask&(1<<HwFilterAll) != 0
}

// ethtoolTsInfo mirrors struct ethtool_ts_info.
type ethtoolTsInfo struct {
	Cmd            uint32
	SoTimestamping uint32
	PhcIndex       int32
	TxTypes        uint32
	TxReserved     [3]uint32
	RxFilters      uint32
	RxReserved     [3]uint32
}

// Timestamping returns the timestamping capabilities of ifname.
func Timestamping(ifname string) (TsInfo, error) {
	r := ethtoolTsInfo{Cmd: ethtoolGetTsInfo}
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&r)); err != nil {
		return TsInfo{PHCIndex: -1}, fmt.Errorf("ETHTOOL_GET_TS_INFO %s: %w", ifname, err)
	}
	return decodeTsInfo(r), nil
}

func decodeTsInfo(r ethtoolTsInfo) TsInfo {
	t := TsInfo{
		PHCIndex:       int(r.PhcIndex),
		SoTimestamping: r.SoTimestamping,
		TxTypesMask:    r.TxTypes,
		RxFiltersMask:  r.RxFilters,
	}
	for _, f := range tsFlagNames {
		if r.SoTimestamping&f.bit != 0 {
			t.Capabilities = append(t.Capabilities, f.name)
		}
	}
	for i, n := range txTypeNames {
		if r.TxTypes&(1<<uint(i)) != 0 {
			t.TxTypes = append(t.TxTypes, n)
		}
	}
	for i, n := range rxFilterNames {
		if r.RxFilters&(1<<uint(i)) != 0 {
			t.RxFilters = append(t.RxFilters, n)
		}
	}
	return t
}

// HwTstampConfig mirrors struct hwtstamp_config.
type HwTstampConfig struct {
	Flags    int32 `json:"flags"`
	TxType   int32 `json:"tx_type"`
	RxFilter int32 `json:"rx_filter"`
}

// GetHwTstamp reads the current hardware timestamping configuration.
func GetHwTstamp(ifname string) (HwTstampConfig, error) {
	var c HwTstampConfig
	err := linux.IoctlIfreqData(ifname, linux.SIOCGHWTSTAMP, unsafe.Pointer(&c))
	return c, err
}

// SetHwTstamp applies a hardware timestamping configuration. The driver may
// adjust RxFilter to a broader filter; the applied value is returned.
// This changes NIC-global state: callers must restore the previous value.
func SetHwTstamp(ifname string, c HwTstampConfig) (HwTstampConfig, error) {
	err := linux.IoctlIfreqData(ifname, linux.SIOCSHWTSTAMP, unsafe.Pointer(&c))
	return c, err
}

// ---------------------------------------------------------------------------
// Offload features (ethtool -k)
// ---------------------------------------------------------------------------

// Feature is the state of one offload feature.
type Feature struct {
	Available bool `json:"available"`
	Requested bool `json:"requested"`
	Active    bool `json:"active"`
	Fixed     bool `json:"fixed"`
}

// Features returns the offload features of ifname keyed by their ethtool
// name (for example "rx-vlan-hw-parse", "rx-vlan-stag-hw-parse").
func Features(ifname string) (map[string]Feature, error) {
	names, err := stringSet(ifname, ethSSFeatures)
	if err != nil {
		return nil, err
	}
	blocks := (len(names) + 31) / 32
	// struct ethtool_gfeatures: cmd u32, size u32, then `size` blocks of
	// {available, requested, active, never_changed} u32.
	buf := make([]byte, 8+blocks*16)
	binary.NativeEndian.PutUint32(buf[0:], ethtoolGFeatures)
	binary.NativeEndian.PutUint32(buf[4:], uint32(blocks))
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&buf[0])); err != nil {
		return nil, fmt.Errorf("ETHTOOL_GFEATURES %s: %w", ifname, err)
	}
	out := make(map[string]Feature, len(names))
	for i, n := range names {
		off := 8 + (i/32)*16
		bit := uint32(1) << uint(i%32)
		avail := binary.NativeEndian.Uint32(buf[off:])
		req := binary.NativeEndian.Uint32(buf[off+4:])
		act := binary.NativeEndian.Uint32(buf[off+8:])
		never := binary.NativeEndian.Uint32(buf[off+12:])
		out[n] = Feature{
			Available: avail&bit != 0,
			Requested: req&bit != 0,
			Active:    act&bit != 0,
			Fixed:     avail&bit == 0 || never&bit != 0,
		}
	}
	return out, nil
}

// stringSet fetches an ethtool string set (ETHTOOL_GSSET_INFO + GSTRINGS).
func stringSet(ifname string, set uint32) ([]string, error) {
	// struct ethtool_sset_info: cmd u32, reserved u32, sset_mask u64, data[] u32
	info := make([]byte, 16+4)
	binary.NativeEndian.PutUint32(info[0:], ethtoolGSsetInfo)
	binary.NativeEndian.PutUint64(info[8:], 1<<set)
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&info[0])); err != nil {
		return nil, fmt.Errorf("ETHTOOL_GSSET_INFO %s: %w", ifname, err)
	}
	if binary.NativeEndian.Uint64(info[8:])&(1<<set) == 0 {
		return nil, fmt.Errorf("%s: string set %d not supported", ifname, set)
	}
	n := int(binary.NativeEndian.Uint32(info[16:]))
	// struct ethtool_gstrings: cmd u32, string_set u32, len u32, data[len*32]
	buf := make([]byte, 12+n*ethGStringLen)
	binary.NativeEndian.PutUint32(buf[0:], ethtoolGStrings)
	binary.NativeEndian.PutUint32(buf[4:], set)
	binary.NativeEndian.PutUint32(buf[8:], uint32(n))
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&buf[0])); err != nil {
		return nil, fmt.Errorf("ETHTOOL_GSTRINGS %s: %w", ifname, err)
	}
	names := make([]string, n)
	for i := 0; i < n; i++ {
		s := buf[12+i*ethGStringLen : 12+(i+1)*ethGStringLen]
		names[i] = strings.TrimRight(string(s), "\x00")
		if j := strings.IndexByte(names[i], 0); j >= 0 {
			names[i] = names[i][:j]
		}
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// Coalescing, rings, channels
// ---------------------------------------------------------------------------

// Coalesce holds the interrupt-coalescing settings relevant to timing
// accuracy (design MR-6).
type Coalesce struct {
	RxUsecs     uint32 `json:"rx_usecs"`
	RxMaxFrames uint32 `json:"rx_max_frames"`
	TxUsecs     uint32 `json:"tx_usecs"`
	TxMaxFrames uint32 `json:"tx_max_frames"`
	AdaptiveRx  bool   `json:"adaptive_rx"`
	AdaptiveTx  bool   `json:"adaptive_tx"`
}

// Coalescing returns ETHTOOL_GCOALESCE settings.
func Coalescing(ifname string) (Coalesce, error) {
	// struct ethtool_coalesce is 23 consecutive u32 values starting with cmd.
	var c [23]uint32
	c[0] = ethtoolGCoalesce
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&c[0])); err != nil {
		return Coalesce{}, fmt.Errorf("ETHTOOL_GCOALESCE %s: %w", ifname, err)
	}
	return Coalesce{
		RxUsecs:     c[1],
		RxMaxFrames: c[2],
		TxUsecs:     c[5],
		TxMaxFrames: c[6],
		AdaptiveRx:  c[10] != 0,
		AdaptiveTx:  c[11] != 0,
	}, nil
}

// Rings holds descriptor ring sizes (ethtool -g).
type Rings struct {
	RxMax uint32 `json:"rx_max"`
	TxMax uint32 `json:"tx_max"`
	Rx    uint32 `json:"rx"`
	Tx    uint32 `json:"tx"`
}

// RingParams returns ETHTOOL_GRINGPARAM.
func RingParams(ifname string) (Rings, error) {
	// cmd, rx_max, rx_mini_max, rx_jumbo_max, tx_max, rx, rx_mini, rx_jumbo, tx
	var r [9]uint32
	r[0] = ethtoolGRingParam
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&r[0])); err != nil {
		return Rings{}, fmt.Errorf("ETHTOOL_GRINGPARAM %s: %w", ifname, err)
	}
	return Rings{RxMax: r[1], TxMax: r[4], Rx: r[5], Tx: r[8]}, nil
}

// Channels holds queue counts (ethtool -l).
type Channels struct {
	MaxCombined uint32 `json:"max_combined"`
	MaxRx       uint32 `json:"max_rx"`
	MaxTx       uint32 `json:"max_tx"`
	Combined    uint32 `json:"combined"`
	Rx          uint32 `json:"rx"`
	Tx          uint32 `json:"tx"`
}

// ChannelCounts returns ETHTOOL_GCHANNELS.
func ChannelCounts(ifname string) (Channels, error) {
	// cmd, max_rx, max_tx, max_other, max_combined, rx, tx, other, combined
	var c [9]uint32
	c[0] = ethtoolGChannels
	if err := linux.IoctlIfreqData(ifname, linux.SIOCETHTOOL, unsafe.Pointer(&c[0])); err != nil {
		return Channels{}, fmt.Errorf("ETHTOOL_GCHANNELS %s: %w", ifname, err)
	}
	return Channels{MaxRx: c[1], MaxTx: c[2], MaxCombined: c[4], Rx: c[5], Tx: c[6], Combined: c[8]}, nil
}

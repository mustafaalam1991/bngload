// Package tstamp implements the Phase 0 timestamp self-test
// (`bngload tstamp check`).
//
// It answers the Phase 0 acceptance question "are hardware timestamps
// verified on RX and TX?" (design section 15) by sending real frames from one
// interface to another and recording, for every frame:
//
//	app TX time   – CLOCK_REALTIME just before sendto()       (user space)
//	TX software   – driver hand-off stamp from the error queue (kernel)
//	TX hardware   – NIC transmit stamp from the error queue    (TX NIC PHC)
//	RX hardware   – NIC receive stamp in SCM_TIMESTAMPING      (RX NIC PHC)
//	RX software   – netif_receive stamp in SCM_TIMESTAMPING    (kernel)
//	app RX time   – CLOCK_REALTIME when recvmsg() returned     (user space)
//
// Intervals are only computed inside one clock domain (MR-1): software
// stamps share CLOCK_REALTIME; hardware stamps are comparable only when TX
// and RX ports share a PTP hardware clock (same PHC index, e.g. two ports of
// one NIC). Across different PHCs only the jitter of the difference is
// reported, never its value.
//
// The test also quantifies why the design measures from kernel/NIC stamps
// instead of user-space clocks: "app send path" and "app RX wake-up" are the
// user-space errors that kernel stamps remove.
//
// Topology: the two interfaces must be connected back-to-back (a cable
// between two test ports, or a veth pair for a software-only rehearsal).
// Frames use the IEEE 802 local experimental Ethertype 0x88B5, unicast to the
// RX port's MAC, at most 1000 frames per second.
package tstamp

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mustafaalam1991/bngload/internal/linux"
	"github.com/mustafaalam1991/bngload/internal/nic"
	"github.com/mustafaalam1991/bngload/internal/pktio/afpacket"
	"github.com/mustafaalam1991/bngload/internal/report"
	"github.com/mustafaalam1991/bngload/internal/stats"
)

// Mode selects which timestamps are requested.
type Mode string

// Modes.
const (
	ModeAuto Mode = "auto" // hardware if both ports advertise it, else software
	ModeHW   Mode = "hw"   // require hardware stamps
	ModeSW   Mode = "sw"   // software stamps only
)

// Config controls a self-test run.
type Config struct {
	TxIf      string        `json:"tx_interface"`
	RxIf      string        `json:"rx_interface"`
	Count     int           `json:"count"`
	Interval  time.Duration `json:"interval_ns"`
	Timeout   time.Duration `json:"timeout_ns"`
	Mode      Mode          `json:"mode_requested"`
	FrameSize int           `json:"frame_size"`
}

// Defaults fills unset fields with safe values.
func (c *Config) Defaults() {
	if c.Count <= 0 {
		c.Count = 200
	}
	if c.Interval < time.Millisecond {
		c.Interval = time.Millisecond // hard ceiling: 1000 frames/s
	}
	if c.Timeout <= 0 {
		c.Timeout = 200 * time.Millisecond
	}
	if c.Mode == "" {
		c.Mode = ModeAuto
	}
	if c.FrameSize < 60 {
		c.FrameSize = 60
	}
	if c.FrameSize > 1514 {
		c.FrameSize = 1514
	}
}

// Sample is the record of one frame. Zero means "not observed".
type Sample struct {
	Seq   uint32 `json:"seq"`
	AppTx int64  `json:"app_tx_ns"`
	TxSW  int64  `json:"tx_sw_ns"`
	TxHW  int64  `json:"tx_hw_ns"`
	RxHW  int64  `json:"rx_hw_ns"`
	RxSW  int64  `json:"rx_sw_ns"`
	AppRx int64  `json:"app_rx_ns"`
}

// Availability counts how many received frames carried each stamp.
type Availability struct {
	Sent      int     `json:"sent"`
	Received  int     `json:"received"`
	TxSW      int     `json:"tx_software"`
	TxHW      int     `json:"tx_hardware"`
	RxSW      int     `json:"rx_software"`
	RxHW      int     `json:"rx_hardware"`
	LossRatio float64 `json:"loss_ratio"`
}

// PortInfo records the timestamping capability of one port.
type PortInfo struct {
	Interface string              `json:"interface"`
	Caps      nic.TsInfo          `json:"timestamping"`
	HwConfig  *nic.HwTstampConfig `json:"hw_config_applied,omitempty"`
	HwError   string              `json:"hw_config_error,omitempty"`
}

// Result is the payload of the tstamp report.
type Result struct {
	Config       Config       `json:"config"`
	ModeUsed     Mode         `json:"mode_used"`
	Tx           PortInfo     `json:"tx_port"`
	Rx           PortInfo     `json:"rx_port"`
	SamePHC      bool         `json:"same_phc"`
	Availability Availability `json:"availability"`
	// One-way latency TX→RX in the software clock domain.
	SoftwareOneWayNs stats.Summary `json:"software_one_way_ns"`
	// One-way latency in the hardware domain (only when SamePHC).
	HardwareOneWayNs *stats.Summary `json:"hardware_one_way_ns,omitempty"`
	// Jitter (deviation from median) of the HW difference across PHCs.
	HardwareCrossPHCJitterNs *stats.Summary `json:"hardware_cross_phc_jitter_ns,omitempty"`
	// User-space errors removed by kernel stamping.
	AppSendPathNs stats.Summary `json:"app_send_path_ns"`
	AppRxWakeupNs stats.Summary `json:"app_rx_wakeup_ns"`
	// NanosecondResolution reports whether stamps carry sub-microsecond
	// digits (some drivers only report microseconds).
	SoftwareNsResolution bool `json:"software_ns_resolution"`
	HardwareNsResolution bool `json:"hardware_ns_resolution"`
	// HardwareRxTxVerified is the Phase 0 acceptance flag: hardware stamps
	// were present on ≥ 99 % of received frames in both directions.
	HardwareRxTxVerified bool             `json:"hardware_rx_tx_verified"`
	Findings             []report.Finding `json:"findings"`
	Samples              []Sample         `json:"samples,omitempty"`
}

// payload layout inside the 0x88B5 frame.
const (
	magic      = "BNGLTS01"
	payloadLen = 8 + 8 + 4 + 8 // magic, nonce, seq, app TX ns
)

// Run executes the self-test. The caller is responsible for the safety
// interlock (safety.Guard.RequireTX) before calling Run.
func Run(cfg Config) (*Result, error) {
	cfg.Defaults()
	res := &Result{Config: cfg, Tx: PortInfo{Interface: cfg.TxIf}, Rx: PortInfo{Interface: cfg.RxIf}}
	if cfg.TxIf == cfg.RxIf {
		return nil, errors.New("TX and RX interfaces must differ (connect two ports back-to-back)")
	}
	var err error
	if res.Tx.Caps, err = nic.Timestamping(cfg.TxIf); err != nil {
		return nil, err
	}
	if res.Rx.Caps, err = nic.Timestamping(cfg.RxIf); err != nil {
		return nil, err
	}
	res.SamePHC = res.Tx.Caps.PHCIndex >= 0 && res.Tx.Caps.PHCIndex == res.Rx.Caps.PHCIndex

	hwCapable := res.Tx.Caps.HasHardware() && res.Rx.Caps.HasHardware()
	switch cfg.Mode {
	case ModeHW:
		if !hwCapable {
			return nil, fmt.Errorf("--mode hw: hardware timestamping not advertised (tx %v, rx %v)",
				res.Tx.Caps.Capabilities, res.Rx.Caps.Capabilities)
		}
		res.ModeUsed = ModeHW
	case ModeSW:
		res.ModeUsed = ModeSW
	default:
		res.ModeUsed = ModeSW
		if hwCapable {
			res.ModeUsed = ModeHW
		}
	}

	if res.ModeUsed == ModeHW {
		restoreTx := enableHW(&res.Tx, true)
		defer restoreTx()
		restoreRx := enableHW(&res.Rx, false)
		defer restoreRx()
	}

	flags := uint32(nic.TsSoftware | nic.TsRxSoftware | nic.TsTxSoftware | nic.TsOptTsOnly)
	if res.ModeUsed == ModeHW {
		flags |= nic.TsRawHardware | nic.TsRxHardware | nic.TsTxHardware
	}
	tx, err := afpacket.Open(cfg.TxIf, afpacket.EthertypeLocalExperimental1)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rx, err := afpacket.Open(cfg.RxIf, afpacket.EthertypeLocalExperimental1)
	if err != nil {
		return nil, err
	}
	defer rx.Close()
	if err := tx.EnableTimestamping(flags); err != nil {
		return nil, err
	}
	if err := rx.EnableTimestamping(flags); err != nil {
		return nil, err
	}

	var nonceB [8]byte
	if _, err := rand.Read(nonceB[:]); err != nil {
		return nil, err
	}
	nonce := binary.BigEndian.Uint64(nonceB[:])
	samples, err := exchange(tx, rx, cfg, nonce, res.ModeUsed == ModeHW)
	if err != nil {
		return nil, err
	}
	analyse(res, samples)
	res.Samples = samples
	return res, nil
}

// enableHW turns on hardware stamping on a port (TX side: TX_ON; RX side:
// FILTER_ALL) and returns a function restoring the previous configuration.
func enableHW(p *PortInfo, txSide bool) func() {
	prev, perr := nic.GetHwTstamp(p.Interface)
	want := nic.HwTstampConfig{TxType: nic.HwTxOff, RxFilter: nic.HwFilterNone}
	if perr == nil {
		want = prev
	}
	if txSide {
		want.TxType = nic.HwTxOn
	} else {
		want.RxFilter = nic.HwFilterAll
	}
	applied, err := nic.SetHwTstamp(p.Interface, want)
	if err != nil {
		p.HwError = fmt.Sprintf("SIOCSHWTSTAMP %+v: %v", want, err)
		return func() {}
	}
	p.HwConfig = &applied
	return func() {
		if perr == nil {
			_, _ = nic.SetHwTstamp(p.Interface, prev)
		}
	}
}

// exchange sends cfg.Count frames one at a time and collects their stamps.
// Sending serially makes TX-stamp attribution unambiguous without relying on
// SOF_TIMESTAMPING_OPT_ID, whose AF_PACKET support varies by kernel.
func exchange(tx, rx *afpacket.Socket, cfg Config, nonce uint64, wantHW bool) ([]Sample, error) {
	buf := make([]byte, 2048)
	oob := make([]byte, 512)
	payload := make([]byte, payloadLen)
	copy(payload, magic)
	binary.BigEndian.PutUint64(payload[8:], nonce)
	warmUp(tx, rx, payload, cfg, nonce, buf, oob)
	out := make([]Sample, 0, cfg.Count)
	for seq := uint32(0); seq < uint32(cfg.Count); seq++ {
		start := time.Now()
		binary.BigEndian.PutUint32(payload[16:], seq)
		appTx, err := linux.ClockGettime(linux.ClockRealtime)
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint64(payload[20:], uint64(appTx))
		frame := afpacket.BuildFrame(rx.MAC, tx.MAC, afpacket.EthertypeLocalExperimental1, payload, cfg.FrameSize)
		s := Sample{Seq: seq, AppTx: appTx}
		if err := tx.Send(frame); err != nil {
			return nil, fmt.Errorf("send on %s: %w", tx.Name, err)
		}
		// Collect the RX frame (skipping frames from other runs or with
		// another sequence number) and the TX stamps, each with a deadline.
		deadline := time.Now().Add(cfg.Timeout)
		for {
			left := time.Until(deadline)
			if left <= 0 {
				break
			}
			n, st, err := rx.Recv(buf, oob, left)
			if err == afpacket.ErrTimeout {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("receive on %s: %w", rx.Name, err)
			}
			appRx, _ := linux.ClockGettime(linux.ClockRealtime)
			if gotSeq, ok := parse(buf[:n], nonce); ok && gotSeq == seq {
				s.RxSW, s.RxHW, s.AppRx = st.Software, st.Hardware, appRx
				break
			}
		}
		// TX stamps may arrive in two messages (software, then hardware);
		// wait for the hardware one only when it was requested.
		txDeadline := time.Now().Add(cfg.Timeout)
		for (s.TxSW == 0 || (wantHW && s.TxHW == 0)) && time.Now().Before(txDeadline) {
			sts, err := tx.RecvTxStamps(time.Until(txDeadline))
			if err == afpacket.ErrTimeout {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("TX stamps on %s: %w", tx.Name, err)
			}
			for _, st := range sts {
				if st.Software != 0 && s.TxSW == 0 {
					s.TxSW = st.Software
				}
				if st.Hardware != 0 && s.TxHW == 0 {
					s.TxHW = st.Hardware
				}
			}
		}
		out = append(out, s)
		if rest := cfg.Interval - time.Since(start); rest > 0 {
			time.Sleep(rest)
		}
	}
	return out, nil
}

// warmUpSeqBase marks warm-up frames; they are never counted.
const warmUpSeqBase = 0xFFFFFF00

// warmUp sends up to 10 uncounted frames until one arrives with a software
// RX stamp. Linux enables RX timestamping through a deferred static key
// (net_enable_timestamp), so the first frame after setting SO_TIMESTAMPING
// can arrive unstamped; without the warm-up that frame would be reported as
// a missing stamp. TX stamps of warm-up frames are drained and discarded.
func warmUp(tx, rx *afpacket.Socket, payload []byte, cfg Config, nonce uint64, buf, oob []byte) {
	for i := uint32(0); i < 10; i++ {
		seq := warmUpSeqBase + i
		binary.BigEndian.PutUint32(payload[16:], seq)
		frame := afpacket.BuildFrame(rx.MAC, tx.MAC, afpacket.EthertypeLocalExperimental1, payload, cfg.FrameSize)
		if tx.Send(frame) != nil {
			return
		}
		stamped := false
		deadline := time.Now().Add(cfg.Timeout)
		for time.Now().Before(deadline) {
			n, st, err := rx.Recv(buf, oob, time.Until(deadline))
			if err != nil {
				break
			}
			if got, ok := parse(buf[:n], nonce); ok && got == seq {
				stamped = st.Software != 0
				break
			}
		}
		_, _ = tx.RecvTxStamps(20 * time.Millisecond)
		if stamped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// parse validates a received frame and returns its sequence number.
func parse(frame []byte, nonce uint64) (uint32, bool) {
	if len(frame) < 14+payloadLen {
		return 0, false
	}
	p := frame[14:]
	if string(p[:8]) != magic || binary.BigEndian.Uint64(p[8:16]) != nonce {
		return 0, false
	}
	return binary.BigEndian.Uint32(p[16:20]), true
}

// analyse derives availability, latency summaries and findings.
func analyse(res *Result, samples []Sample) {
	a := Availability{Sent: len(samples)}
	var sw, hw, send, wake []float64
	swNs, hwNs := false, false
	for _, s := range samples {
		if s.RxSW == 0 && s.RxHW == 0 && s.AppRx == 0 {
			continue
		}
		a.Received++
		if s.TxSW != 0 {
			a.TxSW++
		}
		if s.TxHW != 0 {
			a.TxHW++
		}
		if s.RxSW != 0 {
			a.RxSW++
		}
		if s.RxHW != 0 {
			a.RxHW++
		}
		if s.TxSW != 0 && s.RxSW != 0 {
			sw = append(sw, float64(s.RxSW-s.TxSW))
			swNs = swNs || s.TxSW%1000 != 0 || s.RxSW%1000 != 0
		}
		if s.TxHW != 0 && s.RxHW != 0 {
			hw = append(hw, float64(s.RxHW-s.TxHW))
			hwNs = hwNs || s.TxHW%1000 != 0 || s.RxHW%1000 != 0
		}
		if s.TxSW != 0 {
			send = append(send, float64(s.TxSW-s.AppTx))
		}
		if s.RxSW != 0 && s.AppRx != 0 {
			wake = append(wake, float64(s.AppRx-s.RxSW))
		}
	}
	if a.Sent > 0 {
		a.LossRatio = 1 - float64(a.Received)/float64(a.Sent)
	}
	res.Availability = a
	res.SoftwareOneWayNs = stats.Summarize(sw, "ns")
	res.AppSendPathNs = stats.Summarize(send, "ns")
	res.AppRxWakeupNs = stats.Summarize(wake, "ns")
	res.SoftwareNsResolution = swNs
	res.HardwareNsResolution = hwNs
	if len(hw) > 0 {
		if res.SamePHC {
			s := stats.Summarize(hw, "ns")
			res.HardwareOneWayNs = &s
		} else {
			med := stats.Summarize(hw, "ns").P50
			dev := make([]float64, len(hw))
			for i, v := range hw {
				dev[i] = math.Abs(v - med)
			}
			s := stats.Summarize(dev, "ns")
			res.HardwareCrossPHCJitterNs = &s
		}
	}
	res.HardwareRxTxVerified = a.Received > 0 &&
		stats.Ratio(a.TxHW, a.Received) >= 0.99 && stats.Ratio(a.RxHW, a.Received) >= 0.99
	res.Findings = findings(res)
}

func findings(res *Result) []report.Finding {
	a := res.Availability
	var out []report.Finding
	loss := report.Finding{ID: "tstamp.delivery", Title: "self-test frames delivered TX→RX"}
	switch {
	case a.Received == 0:
		loss.Status = report.Fail
		loss.Detail = "no frames received"
		loss.Remedy = "check cabling between the ports, link state and that both ports are up"
	case a.LossRatio > 0.01:
		loss.Status = report.Fail
		loss.Detail = fmt.Sprintf("%.1f %% loss", 100*a.LossRatio)
	default:
		loss.Status = report.Pass
		loss.Detail = fmt.Sprintf("%d/%d frames", a.Received, a.Sent)
	}
	out = append(out, loss)

	swf := report.Finding{ID: "tstamp.software", Ref: "MR-1", Title: "software RX and TX stamps present"}
	if a.Received > 0 && stats.Ratio(a.RxSW, a.Received) >= 0.99 && stats.Ratio(a.TxSW, a.Received) >= 0.99 {
		swf.Status = report.Pass
		swf.Detail = fmt.Sprintf("one-way p50 %.1f µs, p99 %.1f µs", res.SoftwareOneWayNs.P50/1e3, res.SoftwareOneWayNs.P99/1e3)
	} else {
		swf.Status = report.Fail
		swf.Detail = fmt.Sprintf("rx %d, tx %d of %d", a.RxSW, a.TxSW, a.Received)
		swf.Remedy = "the driver must call skb_tx_timestamp(); check 'ethtool -T' lists software-transmit"
	}
	out = append(out, swf)

	hwf := report.Finding{ID: "tstamp.hardware", Ref: "MR-3", Title: "hardware RX and TX stamps verified (Phase 0 acceptance)"}
	switch {
	case res.HardwareRxTxVerified:
		hwf.Status = report.Pass
		if res.HardwareOneWayNs != nil {
			hwf.Detail = fmt.Sprintf("same PHC, one-way p50 %.0f ns, p99 %.0f ns", res.HardwareOneWayNs.P50, res.HardwareOneWayNs.P99)
		} else if res.HardwareCrossPHCJitterNs != nil {
			hwf.Detail = fmt.Sprintf("different PHCs, difference jitter p99 %.0f ns", res.HardwareCrossPHCJitterNs.P99)
		}
	case res.ModeUsed == ModeSW:
		hwf.Status = report.Warn
		hwf.Detail = "hardware timestamping not used (not advertised by both ports or --mode sw)"
		hwf.Remedy = "use PTP-capable NIC ports for the generator; see docs/phase0/runbook.md"
	default:
		hwf.Status = report.Fail
		hwf.Detail = fmt.Sprintf("hardware stamps: tx %d, rx %d of %d received", a.TxHW, a.RxHW, a.Received)
		if res.Rx.HwError != "" || !res.Rx.Caps.HwFilterAllSupported() {
			hwf.Detail += "; RX port cannot hardware-stamp all frames (PTP-only RX filters)"
		}
		hwf.Remedy = "check 'ethtool -T' rx filters include 'all'; otherwise PPPoE RX stamps will be software only"
	}
	out = append(out, hwf)
	return out
}

// Verdict combines the findings. A software-only run is WARN, not FAIL,
// because it is a valid outcome on hardware without PTP support; the Phase 0
// acceptance report treats it as "criterion not met".
func Verdict(res *Result) report.Verdict { return report.WorstOf(res.Findings) }

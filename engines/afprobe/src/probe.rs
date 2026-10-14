//! Step orchestration, ceiling search and findings.
//!
//! One RX thread owns the TPACKET_V3 ring for the whole run and accounts
//! frames per step (the step number travels in every frame). For each rate
//! step a TX thread is started, the run waits a drain period after it ends,
//! and the RX thread hands over that step's accounting. A step is *clean*
//! when TX reached ≥ 99 % of the target, loss and queue refusals stayed
//! within `max_loss`, and no send error occurred. The highest clean step is
//! the I/O ceiling.

use crate::frame;
use crate::json::Json;
use crate::report::{Finding, Verdict};
use crate::ring::{RingConfig, RxRing};
use crate::seqtrack::SeqTrack;
use crate::stats::{self, Summary};
use crate::sys::{self, PacketSocket, TpacketStatsV3};
use crate::tx::{self, TxPlan, TxResult};
use std::io;
use std::os::fd::AsRawFd;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::{mpsc, Arc};
use std::time::Duration;

/// Probe configuration (see `afprobe run --help` for the meaning of each).
#[derive(Debug, Clone)]
pub struct Config {
    pub tx_if: String,
    pub rx_if: Option<String>,
    pub dst_mac: Option<[u8; 6]>,
    /// Rate steps; `None` is "as fast as possible".
    pub rates: Vec<Option<u64>>,
    pub duration: Duration,
    pub drain: Duration,
    pub frame_size: usize,
    pub batch: usize,
    pub qdisc_bypass: bool,
    pub tx_cpu: Option<usize>,
    pub rx_cpu: Option<usize>,
    pub ring: RingConfig,
    /// Highest tolerated loss (and refusal) ratio for a clean step.
    pub max_loss: f64,
    pub target_pps: Option<u64>,
    /// Frames per PPPoE session setup in one direction, used to express the
    /// ceiling as setups per second.
    pub frames_per_setup: f64,
    /// DUT maximum setup rate (from TS-04) for the design 2.1 check.
    pub dut_max_cps: Option<f64>,
    pub stop_on_fail: bool,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            tx_if: String::new(),
            rx_if: None,
            dst_mac: None,
            rates: [50_000, 100_000, 200_000, 400_000, 800_000, 1_600_000]
                .into_iter()
                .map(Some)
                .chain([None])
                .collect(),
            duration: Duration::from_secs(5),
            drain: Duration::from_millis(300),
            frame_size: 60,
            batch: 32,
            qdisc_bypass: true,
            tx_cpu: None,
            rx_cpu: None,
            ring: RingConfig::default(),
            max_loss: 1e-4,
            target_pps: None,
            frames_per_setup: 13.0,
            dut_max_cps: None,
            stop_on_fail: true,
        }
    }
}

impl Config {
    pub fn to_json(&self) -> Json {
        Json::obj()
            .with("tx_interface", self.tx_if.as_str())
            .with("rx_interface", self.rx_if.clone())
            .with("dst_mac", self.dst_mac.as_ref().map(sys::fmt_mac))
            .with("rates_pps", self.rates.iter().map(|r| Json::from(*r)).collect::<Vec<_>>())
            .with("step_duration_s", self.duration.as_secs_f64())
            .with("drain_s", self.drain.as_secs_f64())
            .with("frame_size", self.frame_size)
            .with("max_batch", self.batch)
            .with("qdisc_bypass", self.qdisc_bypass)
            .with("tx_cpu", self.tx_cpu)
            .with("rx_cpu", self.rx_cpu)
            .with(
                "ring",
                Json::obj()
                    .with("block_size", self.ring.block_size)
                    .with("block_nr", self.ring.block_nr)
                    .with("retire_ms", self.ring.retire_ms),
            )
            .with("max_loss_ratio", self.max_loss)
            .with("target_pps", self.target_pps)
            .with("frames_per_setup", self.frames_per_setup)
            .with("dut_max_setup_rate", self.dut_max_cps)
            .with("stop_on_fail", self.stop_on_fail)
    }
}

/// Facts about one port.
#[derive(Debug, Clone)]
pub struct Port {
    pub name: String,
    pub index: u32,
    pub mac: [u8; 6],
    pub virtual_if: bool,
    pub driver: Option<String>,
    pub numa_node: Option<i64>,
    pub mtu: Option<u64>,
}

impl Port {
    pub fn resolve(name: &str) -> io::Result<Self> {
        Ok(Self {
            name: name.to_string(),
            index: sys::if_index(name)?,
            mac: sys::mac_of(name)?,
            virtual_if: sys::is_virtual(name),
            driver: sys::driver_of(name),
            numa_node: sys::read_attr(format!("/sys/class/net/{name}/device/numa_node")).and_then(|s| s.parse().ok()),
            mtu: sys::read_attr(format!("/sys/class/net/{name}/mtu")).and_then(|s| s.parse().ok()),
        })
    }

    pub fn to_json(&self) -> Json {
        Json::obj()
            .with("name", self.name.as_str())
            .with("ifindex", self.index)
            .with("mac", sys::fmt_mac(&self.mac))
            .with("virtual", self.virtual_if)
            .with("driver", self.driver.clone())
            .with("numa_node", self.numa_node)
            .with("mtu", self.mtu)
    }
}

const NIC_COUNTERS: [&str; 6] =
    ["tx_packets", "tx_dropped", "tx_errors", "rx_packets", "rx_dropped", "rx_missed_errors"];

fn nic_snapshot(ifname: &str) -> Vec<Option<u64>> {
    NIC_COUNTERS.iter().map(|c| sys::nic_counter(ifname, c)).collect()
}

fn nic_delta(ifname: &str, before: &[Option<u64>], after: &[Option<u64>]) -> Json {
    let mut j = Json::obj();
    for (i, c) in NIC_COUNTERS.iter().enumerate() {
        if let (Some(a), Some(b)) = (before[i], after[i]) {
            j.insert(c, b.saturating_sub(a));
        }
    }
    Json::obj().with("interface", ifname).with("counters", j)
}

/// Receive side of one step.
#[derive(Debug, Clone, Default)]
pub struct RxStep {
    pub track: SeqTrack,
    /// Frames of the probe EtherType that did not belong to this run.
    pub foreign: u64,
    pub ring: TpacketStatsV3,
    pub cpu_ns: u64,
}

/// Host-wide CPU use during a step, in cores (1.0 = one CPU fully busy).
#[derive(Debug, Clone, Copy, Default)]
pub struct HostCpu {
    /// All non-idle time, including the TX pacing spin.
    pub busy_cores: f64,
    /// Hard and soft interrupt time: the kernel's packet work outside the
    /// probe threads (NIC RX softirq, veth delivery).
    pub irq_cores: f64,
}

impl HostCpu {
    fn between(a: sys::CpuTimes, b: sys::CpuTimes, cpus: usize) -> Self {
        let dt = b.total.saturating_sub(a.total) as f64;
        let f = |x: u64, y: u64| stats::ratio(y.saturating_sub(x) as f64, dt) * cpus as f64;
        Self { busy_cores: f(a.busy, b.busy), irq_cores: f(a.irq, b.irq) }
    }
}

/// One rate step.
#[derive(Debug, Clone)]
pub struct Step {
    pub index: usize,
    pub target: Option<u64>,
    pub tx: TxResult,
    pub rx: Option<RxStep>,
    pub host: Option<HostCpu>,
    pub nic: Vec<Json>,
    pub ok: bool,
    pub reasons: Vec<String>,
}

impl Step {
    pub fn tx_pps(&self) -> f64 {
        stats::ratio(self.tx.sent as f64, self.tx.wall_ns as f64 / 1e9)
    }
    /// Frames sent per second spent inside `sendmmsg` (pacing waits
    /// excluded): the TX capacity of one core on this path.
    pub fn tx_pps_per_core(&self) -> f64 {
        stats::ratio(self.tx.sent as f64, self.tx.send_ns as f64 / 1e9)
    }
    pub fn rx_pps(&self) -> Option<f64> {
        self.rx.as_ref().and_then(|r| r.track.rate_pps())
    }
    /// Frames received per CPU-second of the RX thread. This is the
    /// user-space ring walk only; the kernel's RX work runs in softirq on
    /// other cores and is reported in [`HostCpu::irq_cores`].
    pub fn rx_pps_per_core(&self) -> Option<f64> {
        self.rx.as_ref().map(|r| stats::ratio(r.track.received as f64, r.cpu_ns as f64 / 1e9))
    }
    pub fn lost(&self) -> Option<i64> {
        self.rx.as_ref().map(|r| self.tx.sent as i64 - r.track.received as i64)
    }
    pub fn loss_ratio(&self) -> Option<f64> {
        self.lost().map(|l| stats::ratio(l.max(0) as f64, self.tx.sent as f64))
    }
    pub fn refused_ratio(&self) -> f64 {
        stats::ratio(self.tx.refused as f64, self.tx.attempted as f64)
    }
    pub fn lag_us(&self) -> Option<Summary> {
        if self.tx.lag_ns.is_empty() {
            return None;
        }
        let us: Vec<f64> = self.tx.lag_ns.iter().map(|n| n / 1e3).collect();
        Some(stats::summarize(&us, "us"))
    }

    fn evaluate(&mut self, max_loss: f64) {
        let mut r = Vec::new();
        if self.tx.sent == 0 {
            r.push("nothing was sent".to_string());
        }
        if let Some(t) = self.target {
            let got = self.tx_pps();
            if got < 0.99 * t as f64 {
                r.push(format!("TX reached {got:.0} of {t} pps"));
            }
        }
        if self.refused_ratio() > max_loss {
            r.push(format!(
                "{} frames refused by the kernel/driver (ENOBUFS), ratio {:.2e}",
                self.tx.refused,
                self.refused_ratio()
            ));
        }
        if self.tx.errors > 0 {
            r.push(format!("{} send errors: {}", self.tx.errors, self.tx.last_error.clone().unwrap_or_default()));
        }
        if let Some(lr) = self.loss_ratio() {
            if lr > max_loss {
                r.push(format!("loss {} frames, ratio {lr:.2e} > {max_loss:.0e}", self.lost().unwrap_or(0)));
            }
        }
        if self.tx.aborted {
            r.push("step aborted".to_string());
        }
        self.ok = r.is_empty();
        self.reasons = r;
    }

    pub fn to_json(&self) -> Json {
        let mut tx = Json::obj()
            .with("attempted", self.tx.attempted)
            .with("sent", self.tx.sent)
            .with("refused_enobufs", self.tx.refused)
            .with("errors", self.tx.errors)
            .with("last_error", self.tx.last_error.clone())
            .with("batch", self.tx.batch)
            .with("wall_s", self.tx.wall_ns as f64 / 1e9)
            .with("cpu_s", self.tx.cpu_ns as f64 / 1e9)
            .with("send_busy_s", self.tx.send_ns as f64 / 1e9)
            .with("achieved_pps", self.tx_pps())
            .with("pps_per_core", self.tx_pps_per_core())
            .with("cpu_utilisation", stats::ratio(self.tx.cpu_ns as f64, self.tx.wall_ns as f64));
        tx.insert("schedule_lag_us", self.lag_us().map(|s| s.to_json()));
        let rx = self.rx.as_ref().map(|r| {
            Json::obj()
                .with("received", r.track.received)
                .with("duplicates", r.track.duplicates)
                .with("out_of_order", r.track.out_of_order)
                .with("foreign_frames", r.foreign)
                .with("lost", self.lost())
                .with("loss_ratio", self.loss_ratio())
                .with("achieved_pps", self.rx_pps())
                .with("cpu_s", r.cpu_ns as f64 / 1e9)
                .with("pps_per_core", self.rx_pps_per_core())
                .with(
                    "ring",
                    Json::obj()
                        .with("packets", r.ring.tp_packets)
                        .with("drops", r.ring.tp_drops)
                        .with("freeze_queue", r.ring.tp_freeze_q_cnt),
                )
        });
        Json::obj()
            .with("index", self.index)
            .with("target_pps", self.target)
            .with("clean", self.ok)
            .with("reasons", self.reasons.iter().map(|s| Json::from(s.as_str())).collect::<Vec<_>>())
            .with("tx", tx)
            .with("rx", rx)
            .with(
                "host_cpu",
                self.host.map(|h| Json::obj().with("busy_cores", h.busy_cores).with("irq_cores", h.irq_cores)),
            )
            .with("nic", self.nic.clone())
    }
}

/// Result of a whole run.
#[derive(Debug, Clone)]
pub struct Outcome {
    pub cfg: Config,
    pub tx_port: Port,
    pub rx_port: Option<Port>,
    pub dst_mac: [u8; 6],
    pub nonce: u64,
    pub steps: Vec<Step>,
    /// Index into `steps` of the highest clean step.
    pub ceiling: Option<usize>,
    /// True when a step failed above the ceiling (the ceiling is measured,
    /// not just a lower bound).
    pub saturated: bool,
    pub interrupted: bool,
    /// The first step lost more than half its frames: the TX and RX ports
    /// are probably not cabled together, and the run was stopped.
    pub cabling_suspect: bool,
    pub findings: Vec<Finding>,
}

impl Outcome {
    pub fn ceiling_pps(&self) -> Option<f64> {
        self.ceiling.map(|i| self.steps[i].tx_pps())
    }

    pub fn to_json(&self) -> Json {
        let ceiling = self.ceiling.map(|i| {
            let s = &self.steps[i];
            Json::obj()
                .with("step", i)
                .with("pps", s.tx_pps())
                .with("tx_pps_per_core", s.tx_pps_per_core())
                .with("rx_pps_per_core", s.rx_pps_per_core())
                .with("setups_per_s_equivalent", s.tx_pps() / self.cfg.frames_per_setup)
                .with("lower_bound_only", !self.saturated)
        });
        Json::obj()
            .with("config", self.cfg.to_json())
            .with("tx_port", self.tx_port.to_json())
            .with("rx_port", self.rx_port.as_ref().map(Port::to_json))
            .with("dst_mac", sys::fmt_mac(&self.dst_mac))
            .with("run_nonce", format!("{:016x}", self.nonce))
            .with("interrupted", self.interrupted)
            .with("cabling_suspect", self.cabling_suspect)
            .with("steps", self.steps.iter().map(Step::to_json).collect::<Vec<_>>())
            .with("ceiling", ceiling)
            .with("findings", self.findings.iter().map(Finding::to_json).collect::<Vec<_>>())
    }
}

/// Shared state between the controller and the RX thread.
struct RxShared {
    stop: AtomicBool,
    /// Number of steps whose accounting the controller has requested.
    requested: AtomicU32,
    /// Latest CPU-time reading of the RX thread.
    cpu_ns: AtomicU64,
}

fn rx_loop(
    mut ring: RxRing,
    nonce: u64,
    steps: usize,
    cpu: Option<usize>,
    sh: Arc<RxShared>,
    out: mpsc::Sender<(SeqTrack, u64)>,
) {
    if let Some(c) = cpu {
        let _ = sys::pin_current_thread(c);
    }
    let mut tracks: Vec<SeqTrack> = (0..steps).map(|_| SeqTrack::default()).collect();
    let mut foreign = 0u64;
    let mut served = 0u32;
    loop {
        let r = ring.drain(5, |pkt, ts| match frame::parse(pkt, nonce) {
            Some((s, q)) if (s as usize) < tracks.len() => tracks[s as usize].record(q, ts),
            _ => foreign += 1,
        });
        if r.is_err() {
            // A ring error ends reception; the controller sees zero frames.
            sh.stop.store(true, Ordering::Release);
        }
        sh.cpu_ns.store(sys::thread_cpu_ns(), Ordering::Release);
        let req = sh.requested.load(Ordering::Acquire);
        while served < req {
            let t = std::mem::take(&mut tracks[served as usize]);
            let _ = out.send((t, foreign));
            foreign = 0;
            served += 1;
        }
        if sh.stop.load(Ordering::Acquire) {
            break;
        }
    }
}

/// Runs the probe. The caller has passed the safety interlock. `progress`
/// receives one line per finished step.
pub fn run(cfg: Config, mut progress: impl FnMut(&Step)) -> Result<Outcome, String> {
    if cfg.rates.is_empty() {
        return Err("no rate steps given".into());
    }
    let tx_port = Port::resolve(&cfg.tx_if).map_err(|e| format!("TX interface: {e}"))?;
    let rx_port = match &cfg.rx_if {
        Some(n) if *n == cfg.tx_if => return Err("TX and RX interfaces must differ".into()),
        Some(n) => Some(Port::resolve(n).map_err(|e| format!("RX interface: {e}"))?),
        None => None,
    };
    let dst_mac = match (cfg.dst_mac, &rx_port) {
        (Some(m), _) => m,
        (None, Some(p)) => p.mac,
        (None, None) => return Err("--dst-mac is required when no --rx-if is given".into()),
    };
    let nonce = sys::random_u64();
    let template = frame::build(dst_mac, tx_port.mac, nonce, cfg.frame_size);

    let txs = PacketSocket::open(tx_port.index, 0).map_err(|e| format!("open TX socket on {}: {e}", tx_port.name))?;
    if cfg.qdisc_bypass {
        let one: libc::c_int = 1;
        txs.set_opt(sys::SOL_PACKET, sys::PACKET_QDISC_BYPASS, &one)
            .map_err(|e| format!("PACKET_QDISC_BYPASS: {e}"))?;
    }
    let sndbuf: libc::c_int = 4 << 20;
    let _ = txs.set_opt(libc::SOL_SOCKET, libc::SO_SNDBUF, &sndbuf);

    // RX thread.
    let shared =
        Arc::new(RxShared { stop: AtomicBool::new(false), requested: AtomicU32::new(0), cpu_ns: AtomicU64::new(0) });
    let (rx_chan_tx, rx_chan) = mpsc::channel();
    let mut rx_thread = None;
    let mut stats_fd = None;
    if let Some(p) = &rx_port {
        let ring = RxRing::open(p.index, frame::ETHERTYPE, cfg.ring)
            .map_err(|e| format!("open TPACKET_V3 ring on {} ({} MiB): {e}", p.name, cfg.ring.bytes() >> 20))?;
        let fd = ring.stats_fd().map_err(|e| e.to_string())?;
        let _ = sys::packet_stats(fd.as_raw_fd()); // reset
        stats_fd = Some(fd);
        let sh = Arc::clone(&shared);
        let (n, cpu) = (cfg.rates.len(), cfg.rx_cpu);
        rx_thread = Some(
            std::thread::Builder::new()
                .name("afprobe-rx".into())
                .spawn(move || rx_loop(ring, nonce, n, cpu, sh, rx_chan_tx))
                .map_err(|e| e.to_string())?,
        );
        std::thread::sleep(Duration::from_millis(20));
    }

    let mut steps: Vec<Step> = Vec::new();
    let mut interrupted = false;
    let mut cabling_suspect = false;
    for (i, &rate) in cfg.rates.iter().enumerate() {
        if sys::STOP.load(Ordering::Relaxed) {
            interrupted = true;
            break;
        }
        let nic_before: Vec<(String, Vec<Option<u64>>)> = std::iter::once(&tx_port)
            .chain(rx_port.as_ref())
            .map(|p| (p.name.clone(), nic_snapshot(&p.name)))
            .collect();
        let rx_cpu0 = shared.cpu_ns.load(Ordering::Acquire);
        let host0 = sys::cpu_times();
        let plan = TxPlan { rate, duration_ns: cfg.duration.as_nanos() as u64, max_batch: cfg.batch, step: i as u32 };
        let stop_tx = AtomicBool::new(false);
        let txr = std::thread::scope(|s| {
            s.spawn(|| {
                if let Some(c) = cfg.tx_cpu {
                    let _ = sys::pin_current_thread(c);
                }
                tx::run(&txs, &template, &plan, &stop_tx)
            })
            .join()
            .unwrap_or_default()
        });
        let host1 = sys::cpu_times();
        std::thread::sleep(cfg.drain);

        let rx = if rx_port.is_some() {
            shared.requested.store(i as u32 + 1, Ordering::Release);
            let (track, foreign) = rx_chan
                .recv_timeout(Duration::from_secs(5))
                .map_err(|_| "RX thread did not report (ring error?)".to_string())?;
            let ring = stats_fd.as_ref().and_then(|fd| sys::packet_stats(fd.as_raw_fd()).ok()).unwrap_or_default();
            let cpu = shared.cpu_ns.load(Ordering::Acquire).saturating_sub(rx_cpu0);
            Some(RxStep { track, foreign, ring, cpu_ns: cpu })
        } else {
            None
        };
        let nic = nic_before.iter().map(|(n, b)| nic_delta(n, b, &nic_snapshot(n))).collect();
        let host = match (host0, host1) {
            (Some(a), Some(b)) => Some(HostCpu::between(a, b, sys::online_cpus())),
            _ => None,
        };
        let mut step = Step { index: i, target: rate, tx: txr, rx, host, nic, ok: false, reasons: Vec::new() };
        step.evaluate(cfg.max_loss);
        progress(&step);
        let failed = !step.ok;
        if step.tx.aborted && sys::STOP.load(Ordering::Relaxed) {
            interrupted = true;
        }
        // If the first step's frames mostly did not come back, the two ports
        // are not cabled to each other: stop even with --no-stop, so higher
        // rates are never sent into an unknown device.
        let not_looped = i == 0 && step.rx.as_ref().is_some_and(|r| r.track.received * 2 < step.tx.sent);
        steps.push(step);
        if not_looped {
            cabling_suspect = true;
            break;
        }
        if interrupted || (failed && cfg.stop_on_fail) {
            break;
        }
    }

    shared.stop.store(true, Ordering::Release);
    if let Some(h) = rx_thread {
        let _ = h.join();
    }

    let ceiling = steps
        .iter()
        .enumerate()
        .filter(|(_, s)| s.ok)
        .max_by(|a, b| a.1.tx_pps().total_cmp(&b.1.tx_pps()))
        .map(|(i, _)| i);
    let saturated = match ceiling {
        Some(c) => steps.iter().skip(c + 1).any(|s| !s.ok) || steps[c].target.is_none(),
        None => true,
    };
    let mut o = Outcome {
        cfg,
        tx_port,
        rx_port,
        dst_mac,
        nonce,
        steps,
        ceiling,
        saturated,
        interrupted,
        cabling_suspect,
        findings: Vec::new(),
    };
    o.findings = findings(&o);
    Ok(o)
}

fn fmt_rate(pps: f64) -> String {
    if pps >= 1e6 {
        format!("{:.2} Mpps", pps / 1e6)
    } else {
        format!("{:.1} kpps", pps / 1e3)
    }
}

/// Derives the findings from a finished run.
pub fn findings(o: &Outcome) -> Vec<Finding> {
    let mut fs = Vec::new();
    let virt: Vec<&str> = std::iter::once(&o.tx_port)
        .chain(o.rx_port.as_ref())
        .filter(|p| p.virtual_if)
        .map(|p| p.name.as_str())
        .collect();
    fs.push(if virt.is_empty() {
        Finding::new(
            "afprobe.ports",
            "Phase 0 host profile",
            "Test ports are physical",
            Verdict::Pass,
            format!(
                "TX {} ({}), RX {}",
                o.tx_port.name,
                o.tx_port.driver.clone().unwrap_or_default(),
                o.rx_port
                    .as_ref()
                    .map(|p| format!("{} ({})", p.name, p.driver.clone().unwrap_or_default()))
                    .unwrap_or("-".into())
            ),
        )
    } else {
        Finding::new(
            "afprobe.ports",
            "Phase 0 host profile",
            "Test ports are physical",
            Verdict::Warn,
            format!("virtual interface(s) {}: the numbers describe the kernel path only, not a NIC", virt.join(", ")),
        )
        .remedy("repeat on the physical test ports cabled back-to-back")
    });

    if o.cabling_suspect {
        fs.push(
            Finding::new(
                "afprobe.cabling",
                "afprobe safety",
                "TX and RX ports are cabled back-to-back",
                Verdict::Fail,
                "fewer than half of the first step's frames arrived on the RX port; the run was stopped",
            )
            .remedy("check that --tx-if and --rx-if are connected to each other and both are up"),
        );
    }
    let Some(ci) = o.ceiling else {
        fs.push(
            Finding::new(
                "afprobe.ceiling",
                "design 2.2",
                "AF_PACKET I/O ceiling",
                Verdict::Fail,
                o.steps.first().map(|s| s.reasons.join("; ")).unwrap_or_else(|| "no step ran".into()),
            )
            .remedy("lower the first rate step, check cabling and that the RX port is up"),
        );
        return fs;
    };
    let c = &o.steps[ci];
    let pps = c.tx_pps();
    let mut detail = format!(
        "{} clean (TX {} per core{}), ≈ {:.0} setups/s at {} frames per setup per direction",
        fmt_rate(pps),
        fmt_rate(c.tx_pps_per_core()),
        c.host
            .map(|h| format!(", host {:.2} cores busy of which {:.2} in IRQ/softirq", h.busy_cores, h.irq_cores))
            .unwrap_or_default(),
        pps / o.cfg.frames_per_setup,
        o.cfg.frames_per_setup
    );
    if !o.saturated {
        detail.push_str("; lower bound — every step was clean, add higher rates or 'max'");
    }
    let status = match o.cfg.target_pps {
        Some(t) if pps >= t as f64 => Verdict::Pass,
        Some(_) => Verdict::Fail,
        None => Verdict::Info,
    };
    let mut f = Finding::new("afprobe.ceiling", "design 2.2", "AF_PACKET I/O ceiling", status, detail);
    if status == Verdict::Fail {
        f = f.remedy("check IRQ/CPU pinning, ring size and NIC queues; see docs/phase0/runbook.md");
    }
    fs.push(f);

    if let Some(dut) = o.cfg.dut_max_cps {
        let need = 3.0 * dut * o.cfg.frames_per_setup;
        fs.push(if pps >= need {
            Finding::new(
                "afprobe.exit_criterion",
                "design 2.1",
                "I/O path can carry 3× the DUT maximum setup rate",
                Verdict::Pass,
                format!(
                    "needs {}, measured {} (necessary, not sufficient: Go workers are measured in TS-16)",
                    fmt_rate(need),
                    fmt_rate(pps)
                ),
            )
        } else {
            Finding::new(
                "afprobe.exit_criterion",
                "design 2.1",
                "I/O path can carry 3× the DUT maximum setup rate",
                Verdict::Warn,
                format!("needs {}, measured {}: even the raw TPACKET_V3 path is short", fmt_rate(need), fmt_rate(pps)),
            )
            .remedy("plan more fan-out workers/queues, or the Phase 7 AF_XDP engine")
        });
    }

    let paced = o.steps.iter().filter(|s| s.ok && s.target.is_some()).max_by_key(|s| s.target);
    if let Some(lag) = paced.and_then(Step::lag_us) {
        let at = fmt_rate(paced.map_or(0.0, |s| s.target.unwrap_or(0) as f64));
        fs.push(if lag.p99 < 1000.0 {
            Finding::new(
                "afprobe.schedule_lag",
                "design 2.1, PR-5",
                "TX schedule lag p99 < 1 ms at the highest clean paced rate",
                Verdict::Pass,
                format!("at {at}: p50 {:.1} µs, p99 {:.1} µs, max {:.1} µs", lag.p50, lag.p99, lag.max),
            )
        } else {
            Finding::new(
                "afprobe.schedule_lag",
                "design 2.1, PR-5",
                "TX schedule lag p99 < 1 ms at the highest clean paced rate",
                Verdict::Warn,
                format!("at {at}: p99 {:.1} µs, max {:.1} µs", lag.p99, lag.max),
            )
            .remedy("pin the TX thread to an isolated core (--tx-cpu) and disable deep C-states")
        });
    }

    if o.rx_port.is_some() {
        let drops: u64 = o.steps[..=ci].iter().filter_map(|s| s.rx.as_ref()).map(|r| r.ring.tp_drops as u64).sum();
        fs.push(if drops == 0 {
            Finding::new(
                "afprobe.ring_drops",
                "MR-6, design 9",
                "No RX ring drops up to the ceiling",
                Verdict::Pass,
                "",
            )
        } else {
            Finding::new(
                "afprobe.ring_drops",
                "MR-6, design 9",
                "No RX ring drops up to the ceiling",
                Verdict::Warn,
                format!("{drops} frames dropped by a full ring"),
            )
            .remedy("increase --ring-blocks/--ring-block-kib or pin the RX thread")
        });
    }

    if o.cfg.tx_cpu.is_none() || (o.rx_port.is_some() && o.cfg.rx_cpu.is_none()) {
        fs.push(
            Finding::new(
                "afprobe.pinning",
                "design 9",
                "Probe threads pinned to cores",
                Verdict::Info,
                "threads were not pinned; per-core figures may vary between runs",
            )
            .remedy("use --tx-cpu/--rx-cpu with isolated cores on the NIC's NUMA node"),
        );
    }
    if o.interrupted {
        fs.push(Finding::new("afprobe.interrupted", "", "Run completed", Verdict::Warn, "stopped by signal"));
    }
    fs
}

#[cfg(test)]
mod tests {
    use super::*;

    fn port(name: &str, virt: bool) -> Port {
        Port {
            name: name.into(),
            index: 1,
            mac: [2, 0, 0, 0, 0, 1],
            virtual_if: virt,
            driver: None,
            numa_node: None,
            mtu: None,
        }
    }

    fn step(i: usize, target: Option<u64>, sent: u64, received: u64, wall_s: f64) -> Step {
        let mut track = SeqTrack::default();
        for q in 0..received as u32 {
            track.record(q, 1 + q as u64);
        }
        let mut s = Step {
            index: i,
            target,
            tx: TxResult {
                attempted: sent,
                sent,
                wall_ns: (wall_s * 1e9) as u64,
                cpu_ns: (wall_s * 0.5e9) as u64,
                send_ns: (wall_s * 0.5e9) as u64,
                lag_ns: vec![1_000.0; 10],
                batch: 32,
                ..Default::default()
            },
            rx: Some(RxStep { track, foreign: 0, ring: TpacketStatsV3::default(), cpu_ns: (wall_s * 0.25e9) as u64 }),
            host: None,
            nic: vec![],
            ok: false,
            reasons: vec![],
        };
        s.evaluate(1e-4);
        s
    }

    #[test]
    fn step_evaluation() {
        let s = step(0, Some(1000), 1000, 1000, 1.0);
        assert!(s.ok, "{:?}", s.reasons);
        assert_eq!(s.tx_pps(), 1000.0);
        assert_eq!(s.tx_pps_per_core(), 2000.0);
        assert_eq!(s.rx_pps_per_core(), Some(4000.0));

        let lossy = step(1, Some(1000), 1000, 990, 1.0);
        assert!(!lossy.ok);
        assert!(lossy.reasons[0].contains("loss 10 frames"), "{:?}", lossy.reasons);

        let slow = step(2, Some(2000), 1000, 1000, 1.0);
        assert!(!slow.ok && slow.reasons[0].contains("TX reached 1000 of 2000"));
    }

    fn outcome(steps: Vec<Step>, cfg: Config) -> Outcome {
        let ceiling = steps.iter().enumerate().rfind(|(_, s)| s.ok).map(|(i, _)| i);
        let saturated = ceiling.map_or(true, |c| steps.iter().skip(c + 1).any(|s| !s.ok));
        let mut o = Outcome {
            cfg,
            tx_port: port("p0", true),
            rx_port: Some(port("p1", true)),
            dst_mac: [2, 0, 0, 0, 0, 2],
            nonce: 1,
            steps,
            ceiling,
            saturated,
            interrupted: false,
            cabling_suspect: false,
            findings: vec![],
        };
        o.findings = findings(&o);
        o
    }

    #[test]
    fn findings_against_target_and_exit_criterion() {
        let cfg =
            Config { target_pps: Some(1500), dut_max_cps: Some(100.0), frames_per_setup: 13.0, ..Default::default() };
        let o = outcome(vec![step(0, Some(1000), 1000, 1000, 1.0), step(1, Some(2000), 2000, 1500, 1.0)], cfg);
        assert_eq!(o.ceiling, Some(0));
        assert!(o.saturated);
        let get = |id: &str| o.findings.iter().find(|f| f.id == id).unwrap().status;
        assert_eq!(get("afprobe.ports"), Verdict::Warn);
        assert_eq!(get("afprobe.ceiling"), Verdict::Fail); // 1000 < 1500
        assert_eq!(get("afprobe.exit_criterion"), Verdict::Warn); // needs 3900 pps
        assert_eq!(get("afprobe.schedule_lag"), Verdict::Pass);
        assert_eq!(get("afprobe.ring_drops"), Verdict::Pass);
        let j = o.to_json().pretty();
        assert!(j.contains("\"setups_per_s_equivalent\""));
    }

    #[test]
    fn no_clean_step_fails() {
        let o = outcome(vec![step(0, Some(1000), 1000, 10, 1.0)], Config::default());
        assert_eq!(o.ceiling, None);
        assert_eq!(o.findings.iter().find(|f| f.id == "afprobe.ceiling").unwrap().status, Verdict::Fail);
    }

    #[test]
    fn all_clean_is_lower_bound() {
        let o = outcome(vec![step(0, Some(1000), 1000, 1000, 1.0)], Config::default());
        let f = o.findings.iter().find(|f| f.id == "afprobe.ceiling").unwrap();
        assert_eq!(f.status, Verdict::Info);
        assert!(f.detail.contains("lower bound"), "{}", f.detail);
    }
}

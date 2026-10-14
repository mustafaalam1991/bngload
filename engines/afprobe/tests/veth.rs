//! End-to-end test over a veth pair. Needs root and iproute2, so it is
//! ignored by default:
//!
//! ```sh
//! sudo -E cargo test --test veth -- --ignored
//! ```

use afprobe::probe::{self, Config};
use std::process::Command;
use std::time::Duration;

struct Veth(&'static str);

impl Veth {
    fn create(a: &'static str, b: &str) -> Veth {
        let _ = Command::new("ip").args(["link", "del", a]).stderr(std::process::Stdio::null()).status();
        let ok = |args: &[&str]| {
            assert!(Command::new("ip").args(args).status().expect("ip(8) missing").success(), "ip {args:?}")
        };
        ok(&["link", "add", a, "type", "veth", "peer", "name", b]);
        // No addresses, no IPv6 autoconf: a probe port carries nothing else.
        for i in [a, b] {
            let _ = std::fs::write(format!("/proc/sys/net/ipv6/conf/{i}/disable_ipv6"), "1");
            ok(&["link", "set", i, "up"]);
        }
        std::thread::sleep(Duration::from_millis(100));
        Veth(a)
    }
}

impl Drop for Veth {
    fn drop(&mut self) {
        let _ = Command::new("ip").args(["link", "del", self.0]).status();
    }
}

fn is_root() -> bool {
    // SAFETY: geteuid has no preconditions.
    unsafe { libc::geteuid() == 0 }
}

#[test]
#[ignore = "needs root: sudo -E cargo test --test veth -- --ignored"]
fn paced_steps_over_veth_are_lossless() {
    assert!(is_root(), "run as root");
    let _v = Veth::create("afp0", "afp1");
    let cfg = Config {
        tx_if: "afp0".into(),
        rx_if: Some("afp1".into()),
        rates: vec![Some(10_000), Some(20_000)],
        duration: Duration::from_millis(500),
        drain: Duration::from_millis(100),
        ..Default::default()
    };
    let mut seen = 0;
    let o = probe::run(cfg, |_| seen += 1).expect("probe run");
    assert_eq!(seen, 2);
    for s in &o.steps {
        let rx = s.rx.as_ref().unwrap();
        assert!(s.ok, "step {} not clean: {:?}", s.index, s.reasons);
        assert_eq!(rx.track.received, s.tx.sent, "every frame arrives on veth");
        assert_eq!(rx.track.duplicates, 0);
        let target = s.target.unwrap() as f64;
        assert!((s.tx_pps() - target).abs() / target < 0.05, "paced rate {} vs {}", s.tx_pps(), target);
        assert!(s.lag_us().unwrap().count > 0);
    }
    assert_eq!(o.ceiling, Some(1));
    assert!(!o.saturated, "all steps clean → lower bound");
}

#[test]
#[ignore = "needs root: sudo -E cargo test --test veth -- --ignored"]
fn unpaced_step_reports_a_ceiling() {
    assert!(is_root(), "run as root");
    let _v = Veth::create("afp2", "afp3");
    let cfg = Config {
        tx_if: "afp2".into(),
        rx_if: Some("afp3".into()),
        rates: vec![None],
        duration: Duration::from_millis(500),
        drain: Duration::from_millis(100),
        max_loss: 0.5, // veth can drop under an unpaced burst; this test checks plumbing
        ..Default::default()
    };
    let o = probe::run(cfg, |_| {}).expect("probe run");
    let s = &o.steps[0];
    assert!(s.tx.sent > 1000, "sent {}", s.tx.sent);
    assert!(s.tx_pps_per_core() > 0.0);
    assert!(s.rx_pps().is_some());
    assert!(o.saturated, "a 'max' step defines the ceiling");
}

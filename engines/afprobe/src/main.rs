//! `afprobe` command-line entry point. See the library documentation
//! (`cargo doc --open`) and `afprobe help`.

use afprobe::cli::{self, Command};
use afprobe::probe::{self, Step};
use afprobe::report::{self, Verdict};
use afprobe::safety::Guard;
use afprobe::{sys, version};
use std::process::ExitCode;
use std::time::SystemTime;

const EXIT_FAIL: u8 = 1;
const EXIT_USAGE: u8 = 2;
const EXIT_ERROR: u8 = 3;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let cmd = match cli::parse(&args) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("afprobe: {e}");
            return ExitCode::from(EXIT_USAGE);
        }
    };
    match cmd {
        Command::Help => {
            print!("{}", cli::USAGE);
            ExitCode::SUCCESS
        }
        Command::Version => {
            println!("afprobe {}", version());
            ExitCode::SUCCESS
        }
        Command::Run { cfg, output, strict } => run(*cfg, output, strict),
    }
}

fn print_header() {
    eprintln!(
        "{:>4} {:>9} {:>10} {:>10} {:>8} {:>8} {:>11} {:>9} {:>5}  status",
        "step", "target", "tx pps", "rx pps", "loss", "lag p99", "tx pps/core", "irq cores", "ring"
    );
}

fn print_step(s: &Step) {
    let target = s.target.map_or("max".to_string(), |t| t.to_string());
    let opt = |v: Option<f64>, d: usize| v.map_or("-".to_string(), |x| format!("{x:.d$}"));
    eprintln!(
        "{:>4} {:>9} {:>10.0} {:>10} {:>8} {:>8} {:>11.0} {:>9} {:>5}  {}",
        s.index,
        target,
        s.tx_pps(),
        opt(s.rx_pps(), 0),
        s.loss_ratio().map_or("-".into(), |l| format!("{l:.1e}")),
        s.lag_us().map_or("-".into(), |l| format!("{:.0}µs", l.p99)),
        s.tx_pps_per_core(),
        opt(s.host.map(|h| h.irq_cores), 2),
        s.rx.as_ref().map_or("-".into(), |r| r.ring.tp_drops.to_string()),
        if s.ok { "clean".to_string() } else { s.reasons.join("; ") }
    );
}

fn run(cfg: probe::Config, output: Option<std::path::PathBuf>, strict: bool) -> ExitCode {
    let guard = Guard::from_env();
    let mut ifaces = vec![cfg.tx_if.as_str()];
    if let Some(r) = &cfg.rx_if {
        ifaces.push(r);
    }
    if let Err(e) = guard.require_tx(&ifaces, sys::addresses_of) {
        eprintln!("afprobe: {e}");
        return ExitCode::from(EXIT_FAIL);
    }
    sys::install_stop_handlers();

    let started = SystemTime::now();
    eprintln!(
        "afprobe {}: {} → {}, {} step(s) of {:?}",
        version(),
        cfg.tx_if,
        cfg.rx_if.as_deref().unwrap_or("(TX only)"),
        cfg.rates.len(),
        cfg.duration
    );
    print_header();
    let outcome = match probe::run(cfg, print_step) {
        Ok(o) => o,
        Err(e) => {
            eprintln!("afprobe: {e}");
            return ExitCode::from(EXIT_ERROR);
        }
    };
    let verdict = report::worst_of(&outcome.findings);
    let summary = match outcome.ceiling_pps() {
        Some(p) => format!("I/O ceiling {p:.0} frames/s{}", if outcome.saturated { "" } else { " (lower bound)" }),
        None => "no clean rate step".to_string(),
    };
    eprintln!();
    for f in &outcome.findings {
        if f.detail.is_empty() {
            eprintln!("[{}] {}", f.status.as_str(), f.title);
        } else {
            eprintln!("[{}] {}: {}", f.status.as_str(), f.title, f.detail);
        }
        if !f.remedy.is_empty() && f.status >= Verdict::Warn {
            eprintln!("       → {}", f.remedy);
        }
    }
    eprintln!("verdict {}: {summary}", verdict.as_str());

    if let Some(path) = output {
        let env =
            report::envelope("afprobe", &version(), started, SystemTime::now(), verdict, &summary, outcome.to_json());
        if let Err(e) = report::write_atomic(&path, &env.pretty()) {
            eprintln!("afprobe: write {}: {e}", path.display());
            return ExitCode::from(EXIT_ERROR);
        }
        if path.as_os_str() != "-" {
            eprintln!("report written to {}", path.display());
        }
    }
    match verdict {
        Verdict::Fail => ExitCode::from(EXIT_FAIL),
        Verdict::Warn if strict => ExitCode::from(EXIT_FAIL),
        _ => ExitCode::SUCCESS,
    }
}

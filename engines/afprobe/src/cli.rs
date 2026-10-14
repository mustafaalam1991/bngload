//! Command-line parsing (hand-written to stay within the `libc`-only rule).

use crate::probe::Config;
use crate::ring::RingConfig;
use crate::sys;
use std::path::PathBuf;
use std::time::Duration;

pub const USAGE: &str = "\
afprobe — AF_PACKET I/O ceiling probe (bngload Phase 0)

USAGE:
    afprobe run --tx-if IF [--rx-if IF] [options]
    afprobe version
    afprobe help

Sends sequence-numbered frames (EtherType 0x88B5) with paced sendmmsg on
--tx-if and receives them through a TPACKET_V3 ring on --rx-if (cable the two
ports back-to-back, or use a veth pair for a dry run). Rates are stepped up
until loss, queue refusals or TX shortfall appear; the highest clean step is
the host's I/O ceiling.

Requires root (CAP_NET_RAW), the lab marker /etc/bngload/lab-host, both
interfaces in /etc/bngload/interfaces.allow and no IP address on them.

RUN OPTIONS:
    --tx-if IF               transmit interface (required)
    --rx-if IF               receive interface; omit for a TX-only run
    --dst-mac MAC            destination MAC (default: MAC of --rx-if;
                             required without --rx-if)
    --rates LIST             comma-separated steps in frames/s with k/M
                             suffixes, 'max' = unpaced
                             [default: 50k,100k,200k,400k,800k,1.6M,max]
    --duration DUR           length of each step (ms, s, m) [default: 5s]
    --drain DUR              wait after each step before counting [default: 300ms]
    --frame-size N           bytes without FCS, 60..1514 [default: 60]
    --batch N                maximum frames per sendmmsg [default: 32]
    --no-qdisc-bypass        send through the qdisc layer
    --tx-cpu N / --rx-cpu N  pin the TX / RX thread to a CPU
    --ring-block-kib N       TPACKET_V3 block size [default: 1024]
    --ring-blocks N          TPACKET_V3 block count [default: 64]
    --block-timeout-ms N     block retire timeout [default: 1]
    --max-loss R             highest loss ratio of a clean step [default: 1e-4]
    --target-pps N           PASS/FAIL the ceiling against this rate
    --frames-per-setup F     frames per session setup per direction, for the
                             setups/s equivalent [default: 13]
    --dut-max-cps R          DUT maximum setup rate from TS-04; checks the
                             design 2.1 exit criterion (3x headroom)
    --tx-only-cap N          highest rate of a TX-only run (no --rx-if), whose
                             frames leave the host; 'max' is refused
                             [default: 20k, the design SR-14 default]
    --no-stop                run every step even after one fails
    -o, --output FILE        write the JSON report (- for stdout)
    --strict                 exit non-zero on WARN as well as FAIL

EXIT CODES: 0 ok, 1 FAIL (or WARN with --strict), 2 usage, 3 error.
";

/// Default rate cap for TX-only runs (design SR-14: 20 kpps until raised).
pub const TX_ONLY_DEFAULT_CAP: u64 = 20_000;

/// A parsed command.
#[derive(Debug)]
pub enum Command {
    Run { cfg: Box<Config>, output: Option<PathBuf>, strict: bool },
    Version,
    Help,
}

/// Parses a rate: `250000`, `250k`, `1.5M`, or `max` (→ `None`).
pub fn parse_rate(s: &str) -> Result<Option<u64>, String> {
    let s = s.trim();
    if s.eq_ignore_ascii_case("max") {
        return Ok(None);
    }
    let (num, mult) = match s.chars().last() {
        Some('k') | Some('K') => (&s[..s.len() - 1], 1e3),
        Some('m') | Some('M') => (&s[..s.len() - 1], 1e6),
        _ => (s, 1.0),
    };
    let v: f64 = num.parse().map_err(|_| format!("invalid rate {s:?}"))?;
    let r = (v * mult).round();
    if !(1.0..=1e9).contains(&r) {
        return Err(format!("rate {s:?} out of range (1 .. 1G frames/s)"));
    }
    Ok(Some(r as u64))
}

/// Parses `500ms`, `5s`, `2m` or plain seconds.
pub fn parse_duration(s: &str) -> Result<Duration, String> {
    let s = s.trim();
    let (num, scale) = if let Some(n) = s.strip_suffix("ms") {
        (n, 1e-3)
    } else if let Some(n) = s.strip_suffix('s') {
        (n, 1.0)
    } else if let Some(n) = s.strip_suffix('m') {
        (n, 60.0)
    } else {
        (s, 1.0)
    };
    let v: f64 = num.parse().map_err(|_| format!("invalid duration {s:?}"))?;
    if !(v.is_finite() && v > 0.0) {
        return Err(format!("duration {s:?} must be positive"));
    }
    Ok(Duration::from_secs_f64(v * scale))
}

fn num<T: std::str::FromStr>(flag: &str, v: &str) -> Result<T, String> {
    v.parse().map_err(|_| format!("{flag}: invalid value {v:?}"))
}

/// Parses the full argument list (without the program name).
pub fn parse(args: &[String]) -> Result<Command, String> {
    let Some(cmd) = args.first() else { return Ok(Command::Help) };
    match cmd.as_str() {
        "help" | "-h" | "--help" => return Ok(Command::Help),
        "version" | "--version" => return Ok(Command::Version),
        "run" => {}
        other => return Err(format!("unknown command {other:?} (try 'afprobe help')")),
    }
    let mut cfg = Config::default();
    let mut ring = RingConfig::default();
    let mut output = None;
    let mut strict = false;
    let mut tx_only_cap: u64 = TX_ONLY_DEFAULT_CAP;
    let mut it = args[1..].iter();
    while let Some(arg) = it.next() {
        let (flag, inline) = match arg.split_once('=') {
            Some((f, v)) if f.starts_with("--") => (f, Some(v.to_string())),
            _ => (arg.as_str(), None),
        };
        let mut val = || -> Result<String, String> {
            inline.clone().or_else(|| it.next().cloned()).ok_or_else(|| format!("{flag} needs a value"))
        };
        match flag {
            "-h" | "--help" => return Ok(Command::Help),
            "--tx-if" => cfg.tx_if = val()?,
            "--rx-if" => cfg.rx_if = Some(val()?),
            "--dst-mac" => {
                let v = val()?;
                cfg.dst_mac = Some(sys::parse_mac(&v).ok_or_else(|| format!("--dst-mac: invalid MAC {v:?}"))?);
            }
            "--rates" => {
                cfg.rates =
                    val()?.split(',').filter(|s| !s.trim().is_empty()).map(parse_rate).collect::<Result<_, _>>()?;
                if cfg.rates.is_empty() {
                    return Err("--rates is empty".into());
                }
            }
            "--duration" => cfg.duration = parse_duration(&val()?)?,
            "--drain" => cfg.drain = parse_duration(&val()?)?,
            "--frame-size" => {
                cfg.frame_size = num(flag, &val()?)?;
                if !(60..=1514).contains(&cfg.frame_size) {
                    return Err("--frame-size must be 60..1514".into());
                }
            }
            "--batch" => {
                cfg.batch = num(flag, &val()?)?;
                if !(1..=1024).contains(&cfg.batch) {
                    return Err("--batch must be 1..1024".into());
                }
            }
            "--no-qdisc-bypass" => cfg.qdisc_bypass = false,
            "--tx-cpu" => cfg.tx_cpu = Some(num(flag, &val()?)?),
            "--rx-cpu" => cfg.rx_cpu = Some(num(flag, &val()?)?),
            "--ring-block-kib" => ring.block_size = num::<usize>(flag, &val()?)? * 1024,
            "--ring-blocks" => ring.block_nr = num(flag, &val()?)?,
            "--block-timeout-ms" => ring.retire_ms = num(flag, &val()?)?,
            "--max-loss" => {
                cfg.max_loss = num(flag, &val()?)?;
                if !(0.0..1.0).contains(&cfg.max_loss) {
                    return Err("--max-loss must be in [0, 1)".into());
                }
            }
            "--target-pps" => cfg.target_pps = parse_rate(&val()?)?,
            "--frames-per-setup" => {
                cfg.frames_per_setup = num(flag, &val()?)?;
                if cfg.frames_per_setup.is_nan() || cfg.frames_per_setup <= 0.0 {
                    return Err("--frames-per-setup must be positive".into());
                }
            }
            "--dut-max-cps" => cfg.dut_max_cps = Some(num(flag, &val()?)?),
            "--no-stop" => cfg.stop_on_fail = false,
            "--tx-only-cap" => {
                tx_only_cap = parse_rate(&val()?)?.ok_or("--tx-only-cap cannot be 'max'")?;
            }
            "-o" | "--output" => output = Some(PathBuf::from(val()?)),
            "--strict" => strict = true,
            other => return Err(format!("unknown option {other:?} (try 'afprobe help')")),
        }
    }
    ring.validate().map_err(|e| e.to_string())?;
    cfg.ring = ring;
    if cfg.tx_if.is_empty() {
        return Err("--tx-if is required".into());
    }
    if cfg.rx_if.is_none() {
        if cfg.dst_mac.is_none() {
            return Err("--dst-mac is required for a TX-only run (no --rx-if)".into());
        }
        // Frames of a TX-only run leave the host towards whatever is cabled
        // to the port, so the rate is capped (design SR-14 default 20 kpps).
        for r in &cfg.rates {
            match r {
                None => return Err("'max' is not allowed in a TX-only run (SR-14); give explicit --rates".into()),
                Some(r) if *r > tx_only_cap => {
                    return Err(format!(
                    "rate {r} exceeds the TX-only cap of {tx_only_cap} frames/s (SR-14); raise it with --tx-only-cap"
                ))
                }
                _ => {}
            }
        }
    }
    Ok(Command::Run { cfg: Box::new(cfg), output, strict })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(s: &str) -> Vec<String> {
        s.split_whitespace().map(String::from).collect()
    }

    #[test]
    fn rates() {
        assert_eq!(parse_rate("50k"), Ok(Some(50_000)));
        assert_eq!(parse_rate("1.6M"), Ok(Some(1_600_000)));
        assert_eq!(parse_rate("1200"), Ok(Some(1200)));
        assert_eq!(parse_rate("MAX"), Ok(None));
        assert!(parse_rate("0").is_err());
        assert!(parse_rate("fast").is_err());
    }

    #[test]
    fn durations() {
        assert_eq!(parse_duration("300ms"), Ok(Duration::from_millis(300)));
        assert_eq!(parse_duration("5s"), Ok(Duration::from_secs(5)));
        assert_eq!(parse_duration("2m"), Ok(Duration::from_secs(120)));
        assert_eq!(parse_duration("1.5"), Ok(Duration::from_millis(1500)));
        assert!(parse_duration("-1s").is_err());
        assert!(parse_duration("soon").is_err());
    }

    #[test]
    fn full_command_line() {
        let c = parse(&args(
            "run --tx-if p0 --rx-if=p1 --rates 10k,max --duration 1s --tx-cpu 2 --rx-cpu 3 --ring-block-kib 4096 --target-pps 1M -o r.json --strict",
        ))
        .unwrap();
        let Command::Run { cfg, output, strict } = c else { panic!() };
        assert_eq!(cfg.tx_if, "p0");
        assert_eq!(cfg.rx_if.as_deref(), Some("p1"));
        assert_eq!(cfg.rates, vec![Some(10_000), None]);
        assert_eq!(cfg.duration, Duration::from_secs(1));
        assert_eq!((cfg.tx_cpu, cfg.rx_cpu), (Some(2), Some(3)));
        assert_eq!(cfg.ring.block_size, 4 << 20);
        assert_eq!(cfg.target_pps, Some(1_000_000));
        assert_eq!(output, Some(PathBuf::from("r.json")));
        assert!(strict);
    }

    #[test]
    fn errors() {
        assert!(parse(&args("run")).unwrap_err().contains("--tx-if"));
        assert!(parse(&args("run --tx-if p0")).unwrap_err().contains("--dst-mac"));
        assert!(parse(&args("run --tx-if p0 --dst-mac 02:00:00:00:00:01")).unwrap_err().contains("SR-14"));
        assert!(parse(&args("run --tx-if p0 --dst-mac 02:00:00:00:00:01 --rates 10k,20k")).is_ok());
        assert!(parse(&args("run --tx-if p0 --dst-mac 02:00:00:00:00:01 --rates 10k,max"))
            .unwrap_err()
            .contains("'max' is not allowed"));
        assert!(parse(&args("run --tx-if p0 --dst-mac 02:00:00:00:00:01 --rates 50k"))
            .unwrap_err()
            .contains("TX-only cap"));
        assert!(parse(&args("run --tx-if p0 --dst-mac 02:00:00:00:00:01 --rates 50k --tx-only-cap 100k")).is_ok());
        assert!(parse(&args("run --tx-if p0 --rx-if p1 --bogus")).unwrap_err().contains("unknown option"));
        assert!(parse(&args("run --tx-if p0 --rx-if p1 --frame-size 20")).is_err());
        assert!(parse(&args("run --tx-if p0 --rx-if p1 --ring-block-kib 3")).is_err());
        assert!(parse(&args("run --tx-if")).unwrap_err().contains("needs a value"));
        assert!(parse(&args("frobnicate")).is_err());
        assert!(matches!(parse(&[]), Ok(Command::Help)));
    }
}

//! Safety interlocks, identical in rule and wording to the Go
//! `internal/safety` package (design section 13: SR-11 and the Phase 0 subset
//! of SR-19):
//!
//! 1. the lab marker `/etc/bngload/lab-host` must exist;
//! 2. every interface used must be listed in `/etc/bngload/interfaces.allow`;
//! 3. a test interface must carry no IPv4 address and no global IPv6 address.
//!
//! `BNGLOAD_LAB_MARKER`, `BNGLOAD_INTERFACE_ALLOWLIST` and
//! `BNGLOAD_ALLOWED_INTERFACES` override the defaults for CI and tests.

use std::io::{self, BufRead};
use std::net::IpAddr;
use std::path::PathBuf;

pub const DEFAULT_LAB_MARKER: &str = "/etc/bngload/lab-host";
pub const DEFAULT_ALLOWLIST: &str = "/etc/bngload/interfaces.allow";

/// Resolved safety configuration.
#[derive(Debug, Clone)]
pub struct Guard {
    pub lab_marker: PathBuf,
    pub allowlist_path: PathBuf,
    /// Inline allowlist; replaces the file when non-empty.
    pub allowed: Vec<String>,
}

/// The lab marker and its optional `key=value` fields.
#[derive(Debug, Clone, Default)]
pub struct LabMarker {
    pub path: String,
    pub present: bool,
    pub fields: Vec<(String, String)>,
}

impl Guard {
    /// Defaults plus environment overrides.
    pub fn from_env() -> Self {
        Self::from_lookup(|k| std::env::var(k).ok())
    }

    /// Builds a guard from an arbitrary variable lookup (testable).
    pub fn from_lookup(get: impl Fn(&str) -> Option<String>) -> Self {
        let nonempty = |k: &str| get(k).filter(|v| !v.is_empty());
        Self {
            lab_marker: nonempty("BNGLOAD_LAB_MARKER").unwrap_or_else(|| DEFAULT_LAB_MARKER.into()).into(),
            allowlist_path: nonempty("BNGLOAD_INTERFACE_ALLOWLIST").unwrap_or_else(|| DEFAULT_ALLOWLIST.into()).into(),
            allowed: nonempty("BNGLOAD_ALLOWED_INTERFACES")
                .map(|v| v.split(',').map(|s| s.trim().to_string()).filter(|s| !s.is_empty()).collect())
                .unwrap_or_default(),
        }
    }

    /// Reads the lab marker.
    pub fn lab_marker(&self) -> LabMarker {
        let mut m = LabMarker { path: self.lab_marker.display().to_string(), ..Default::default() };
        let Ok(f) = std::fs::File::open(&self.lab_marker) else { return m };
        m.present = true;
        for line in io::BufReader::new(f).lines().map_while(Result::ok) {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            if let Some((k, v)) = line.split_once('=') {
                m.fields.push((k.trim().to_string(), v.trim().to_string()));
            }
        }
        m
    }

    /// The effective, sorted allowlist. A missing file is an empty list.
    pub fn allowed_interfaces(&self) -> io::Result<Vec<String>> {
        if !self.allowed.is_empty() {
            return Ok(self.allowed.clone());
        }
        let f = match std::fs::File::open(&self.allowlist_path) {
            Ok(f) => f,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(e) => return Err(e),
        };
        let mut out = Vec::new();
        for line in io::BufReader::new(f).lines() {
            let line = line?;
            let name = line.split('#').next().unwrap_or("").trim();
            if !name.is_empty() {
                out.push(name.to_string());
            }
        }
        out.sort();
        Ok(out)
    }

    /// Verifies that frames may be transmitted on all `ifaces`. `addrs`
    /// returns the addresses configured on an interface. On refusal the error
    /// lists every violated rule with its remedy.
    pub fn require_tx(&self, ifaces: &[&str], addrs: impl Fn(&str) -> io::Result<Vec<IpAddr>>) -> Result<(), String> {
        let mut problems = Vec::new();
        if !self.lab_marker().present {
            problems.push(format!(
                "lab marker {0} is missing (create it on authorised lab hosts only: sudo install -D -m 0644 /dev/null {0})",
                self.lab_marker.display()
            ));
        }
        let allowed = self.allowed_interfaces();
        for ifname in ifaces {
            match &allowed {
                Err(e) => {
                    problems.push(format!("cannot read interface allowlist {}: {e}", self.allowlist_path.display()))
                }
                Ok(list) if !list.iter().any(|n| n == ifname) => problems.push(format!(
                    "interface {ifname} is not in the allowlist {} (add one interface name per line)",
                    self.allowlist_path.display()
                )),
                Ok(_) => {}
            }
            match addrs(ifname) {
                Err(e) => problems.push(format!("cannot list addresses of {ifname}: {e}")),
                Ok(a) => {
                    let routable: Vec<String> =
                        a.iter().filter(|ip| is_routable(ip)).map(|ip| ip.to_string()).collect();
                    if !routable.is_empty() {
                        problems.push(format!(
                            "interface {ifname} carries routable addresses [{}]; a test port must have no IP address (SR-19)",
                            routable.join(" ")
                        ));
                    }
                }
            }
        }
        if problems.is_empty() {
            Ok(())
        } else {
            Err(format!("refused by safety interlock:\n  - {}", problems.join("\n  - ")))
        }
    }
}

/// Any IPv4 address, and any IPv6 address other than loopback, link-local
/// (fe80::/10), multicast and unspecified, marks a port as routed.
pub fn is_routable(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(_) => true,
        IpAddr::V6(a) => {
            let link_local = (a.segments()[0] & 0xffc0) == 0xfe80;
            !(a.is_loopback() || a.is_multicast() || a.is_unspecified() || link_local)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("afprobe-test-{}-{name}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    fn no_addrs(_: &str) -> io::Result<Vec<IpAddr>> {
        Ok(vec!["fe80::1".parse().unwrap()])
    }

    #[test]
    fn marker_and_allowlist() {
        let d = tmpdir("marker");
        let g = Guard { lab_marker: d.join("lab-host"), allowlist_path: d.join("allow"), allowed: vec![] };
        assert!(!g.lab_marker().present);
        let err = g.require_tx(&["p0"], no_addrs).unwrap_err();
        assert!(err.contains("lab marker") && err.contains("not in the allowlist"), "{err}");

        std::fs::write(&g.lab_marker, "# lab\nlab = doha-perf-1\n").unwrap();
        std::fs::write(&g.allowlist_path, "p1 # second\n\n# c\np0\n").unwrap();
        let m = g.lab_marker();
        assert!(m.present);
        assert_eq!(m.fields, vec![("lab".to_string(), "doha-perf-1".to_string())]);
        assert_eq!(g.allowed_interfaces().unwrap(), vec!["p0", "p1"]);
        assert!(g.require_tx(&["p0", "p1"], no_addrs).is_ok());
        assert!(g.require_tx(&["eth0"], no_addrs).is_err());
        let _ = std::fs::remove_dir_all(d);
    }

    #[test]
    fn routable_addresses_refused() {
        let d = tmpdir("routable");
        std::fs::write(d.join("lab-host"), "").unwrap();
        let g = Guard { lab_marker: d.join("lab-host"), allowlist_path: d.join("none"), allowed: vec!["p0".into()] };
        let with_v4 = |_: &str| Ok(vec!["192.0.2.10".parse().unwrap()]);
        let err = g.require_tx(&["p0"], with_v4).unwrap_err();
        assert!(err.contains("192.0.2.10") && err.contains("SR-19"), "{err}");
        let _ = std::fs::remove_dir_all(d);
    }

    #[test]
    fn routable_classification() {
        for (a, want) in [
            ("10.0.0.1", true),
            ("127.0.0.1", true),
            ("2001:db8::1", true),
            ("fd00::1", true),
            ("fe80::1", false),
            ("::1", false),
            ("ff02::1", false),
        ] {
            assert_eq!(is_routable(&a.parse().unwrap()), want, "{a}");
        }
    }

    #[test]
    fn env_overrides() {
        let vars: HashMap<&str, &str> =
            [("BNGLOAD_LAB_MARKER", "/x/m"), ("BNGLOAD_ALLOWED_INTERFACES", "a, b,")].into_iter().collect();
        let g = Guard::from_lookup(|k| vars.get(k).map(|v| v.to_string()));
        assert_eq!(g.lab_marker, PathBuf::from("/x/m"));
        assert_eq!(g.allowlist_path, PathBuf::from(DEFAULT_ALLOWLIST));
        assert_eq!(g.allowed, vec!["a", "b"]);
    }
}

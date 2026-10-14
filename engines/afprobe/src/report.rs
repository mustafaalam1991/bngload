//! The `bngload.report/v1` envelope shared by every Phase 0 tool, so that the
//! acceptance report can aggregate Go, Rust and Python results uniformly.

use crate::json::Json;
use std::io::{self, Write};
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

pub const SCHEMA: &str = "bngload.report/v1";

/// Overall or per-finding status, ordered by severity.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Verdict {
    Info,
    Pass,
    Warn,
    Fail,
}

impl Verdict {
    pub fn as_str(self) -> &'static str {
        match self {
            Verdict::Info => "INFO",
            Verdict::Pass => "PASS",
            Verdict::Warn => "WARN",
            Verdict::Fail => "FAIL",
        }
    }
}

/// One checked statement, as in the Go `report.Finding`.
#[derive(Debug, Clone)]
pub struct Finding {
    pub id: String,
    pub reference: String,
    pub title: String,
    pub status: Verdict,
    pub detail: String,
    pub remedy: String,
}

impl Finding {
    pub fn new(id: &str, reference: &str, title: &str, status: Verdict, detail: impl Into<String>) -> Self {
        Self {
            id: id.into(),
            reference: reference.into(),
            title: title.into(),
            status,
            detail: detail.into(),
            remedy: String::new(),
        }
    }

    pub fn remedy(mut self, r: impl Into<String>) -> Self {
        self.remedy = r.into();
        self
    }

    pub fn to_json(&self) -> Json {
        let mut j = Json::obj().with("id", self.id.as_str());
        if !self.reference.is_empty() {
            j.insert("ref", self.reference.as_str());
        }
        j.insert("title", self.title.as_str());
        j.insert("status", self.status.as_str());
        if !self.detail.is_empty() {
            j.insert("detail", self.detail.as_str());
        }
        if !self.remedy.is_empty() {
            j.insert("remedy", self.remedy.as_str());
        }
        j
    }
}

/// Most severe status (INFO when empty).
pub fn worst_of(fs: &[Finding]) -> Verdict {
    fs.iter().map(|f| f.status).max().unwrap_or(Verdict::Info)
}

/// RFC 3339 UTC timestamp with nanoseconds, as Go's `time.Time` encodes.
pub fn rfc3339(t: SystemTime) -> String {
    let d = t.duration_since(UNIX_EPOCH).unwrap_or_default();
    let secs = d.as_secs() as i64;
    let (days, rem) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    let (y, m, dd) = civil_from_days(days);
    format!("{y:04}-{m:02}-{dd:02}T{:02}:{:02}:{:02}.{:09}Z", rem / 3600, (rem % 3600) / 60, rem % 60, d.subsec_nanos())
}

/// Days since 1970-01-01 → (year, month, day). Howard Hinnant's algorithm.
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

/// Builds the envelope.
pub fn envelope(
    tool: &str,
    version: &str,
    started: SystemTime,
    finished: SystemTime,
    verdict: Verdict,
    summary: &str,
    data: Json,
) -> Json {
    let (hostname, kernel) = crate::sys::uname();
    Json::obj()
        .with("schema", SCHEMA)
        .with("tool", tool)
        .with("version", version)
        .with("host", Json::obj().with("hostname", hostname).with("kernel", kernel))
        .with("started_at", rfc3339(started))
        .with("finished_at", rfc3339(finished))
        .with("verdict", verdict.as_str())
        .with("summary", summary)
        .with("data", data)
}

/// Writes `content` atomically (temporary file + rename), creating parent
/// directories. `-` writes to stdout.
pub fn write_atomic(path: &Path, content: &str) -> io::Result<()> {
    if path.as_os_str() == "-" {
        return io::stdout().write_all(content.as_bytes());
    }
    if let Some(dir) = path.parent().filter(|p| !p.as_os_str().is_empty()) {
        std::fs::create_dir_all(dir)?;
    }
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, content)?;
    std::fs::rename(&tmp, path)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn rfc3339_known_instants() {
        assert_eq!(rfc3339(UNIX_EPOCH), "1970-01-01T00:00:00.000000000Z");
        let t = UNIX_EPOCH + Duration::new(951_782_400, 5); // leap day 2000
        assert_eq!(rfc3339(t), "2000-02-29T00:00:00.000000005Z");
        let t = UNIX_EPOCH + Duration::from_secs(1_791_936_000 + 3661);
        assert_eq!(rfc3339(t), "2026-10-14T01:01:01.000000000Z");
    }

    #[test]
    fn verdict_ordering() {
        let f = |s| Finding::new("x", "", "t", s, "");
        assert_eq!(worst_of(&[]), Verdict::Info);
        assert_eq!(worst_of(&[f(Verdict::Pass), f(Verdict::Warn), f(Verdict::Info)]), Verdict::Warn);
        assert_eq!(worst_of(&[f(Verdict::Fail), f(Verdict::Warn)]), Verdict::Fail);
    }

    #[test]
    fn envelope_shape_and_atomic_write() {
        let e = envelope("afprobe", "0.0.0", UNIX_EPOCH, UNIX_EPOCH, Verdict::Pass, "ok", Json::obj());
        assert_eq!(e.get("schema"), Some(&Json::from(SCHEMA)));
        assert_eq!(e.get("verdict"), Some(&Json::from("PASS")));
        let dir = std::env::temp_dir().join(format!("afprobe-report-{}", std::process::id()));
        let p = dir.join("sub/r.json");
        write_atomic(&p, &e.pretty()).unwrap();
        assert!(std::fs::read_to_string(&p).unwrap().contains("\"tool\": \"afprobe\""));
        assert!(!p.with_extension("json.tmp").exists());
        let _ = std::fs::remove_dir_all(dir);
    }
}

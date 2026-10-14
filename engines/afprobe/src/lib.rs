//! # afprobe — AF_PACKET I/O ceiling probe
//!
//! `afprobe` answers one Phase 0 question from the bngload design (section
//! 2.2, "scale ceiling … validate in Phase 0"): **how many frames per second
//! can this host move through the AF_PACKET path that the bngload engine will
//! use, and at what CPU cost?**
//!
//! It transmits paced, sequence-numbered frames with `sendmmsg` on one test
//! port and receives them through a `TPACKET_V3` memory-mapped ring on a second
//! port that is cabled back-to-back (or on the peer of a veth pair for a dry
//! run). The rate is stepped up until loss, queue refusals or TX shortfall
//! appear. The highest clean step is the *I/O ceiling*.
//!
//! The probe is written in Rust on purpose (ADR-0001): it has no garbage
//! collector and no runtime scheduler, so its ceiling is the reference against
//! which the Go engine is compared later (design 2.1 exit criterion, TS-16).
//! A Go engine reaching a similar per-core rate shows the GC is not the limit;
//! a large gap is an early warning.
//!
//! ## Module map
//!
//! | Module | Role |
//! | --- | --- |
//! | [`sys`] | Thin, audited wrappers around the Linux system calls used |
//! | [`ring`] | `TPACKET_V3` RX ring: setup, block walk, release |
//! | [`tx`] | Paced `sendmmsg` transmitter with schedule-lag recording |
//! | [`frame`] | Probe frame layout (EtherType 0x88B5) |
//! | [`seqtrack`] | Per-step loss, duplicate and reordering accounting |
//! | [`probe`] | Step orchestration, ceiling search and findings |
//! | [`safety`] | Lab-host and interface interlocks (shared rules with the Go CLI) |
//! | [`report`] | `bngload.report/v1` envelope and findings |
//! | [`json`], [`stats`] | Dependency-free JSON writer and percentiles |
//! | [`cli`] | Command-line parsing |
//!
//! All `unsafe` code lives in [`sys`], [`ring`] and [`tx`] and is limited to
//! system calls and the shared-memory ring.

pub mod cli;
pub mod frame;
pub mod json;
pub mod probe;
pub mod report;
pub mod ring;
pub mod safety;
pub mod seqtrack;
pub mod stats;
pub mod sys;
pub mod tx;

/// Version string, including the git commit when the build sets
/// `AFPROBE_COMMIT` (the Makefile does).
pub fn version() -> String {
    match option_env!("AFPROBE_COMMIT") {
        Some(c) if !c.is_empty() => format!("{} ({})", env!("CARGO_PKG_VERSION"), c),
        _ => env!("CARGO_PKG_VERSION").to_string(),
    }
}

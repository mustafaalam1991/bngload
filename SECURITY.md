# Security Policy

bngload runs as **root** on lab generator hosts and transmits raw Ethernet
frames. For this project a security issue is anything that lets the tools act
outside the lab they were authorised for, gives someone else control of a
root process on the generator host, or weakens the supply chain of the code
that runs there.

## Supported Versions

bngload is pre-release (Phase 0, version 0.x) and has no tagged releases yet.
Security fixes land on `main` only.

| Version | Supported |
| ------- | --------- |
| `main` — latest commit (bngload 0.0.0-phase0, afprobe 0.1.0, bngload-lab 0.1.0) | :white_check_mark: |
| Older commits, other branches and forks | :x: |

When releases are tagged, the latest release line and `main` will be
supported, and this table will list them. Always update to the latest commit
before reporting; the issue may already be fixed.

## Reporting a Vulnerability

**Please do not report security issues in public issues, pull requests or
discussions.**

1. Report privately through GitHub: open the repository's **Security** tab
   and choose **Report a vulnerability**
   (<https://github.com/mustafaalam1991/bngload/security/advisories/new>).
2. If that option is not available, contact the maintainer,
   [@mustafaalam1991](https://github.com/mustafaalam1991), through GitHub and
   ask for a private channel. Do not include vulnerability details in that
   first message.

Please include:

- the component (`bngload`, `afprobe`, `bngload-lab`, a script, CI) and the
  commit you tested;
- what an attacker can do, and what access they need to start with;
- steps to reproduce, with kernel version and, if relevant, NIC model, driver
  and firmware (`ethtool -i <if>`);
- a proof of concept, if you have one, and any fix you suggest;
- whether you have told anyone else.

Test only on hosts and networks you own or are authorised to use. Never test
against production networks, live subscribers or a DUT you have no permission
to load.

### What to expect

bngload is maintained by a small team, so these are best-effort targets:

| Stage | Target |
| --- | --- |
| Acknowledgement of your report | within 5 business days |
| Assessment: accepted or declined, with severity | within 14 days |
| Status updates while the report is open | at least every 14 days |
| Fix on `main` — critical or high severity | within 30 days of acceptance |
| Fix on `main` — medium or low severity | within 90 days of acceptance |

**If the report is accepted:** we agree a disclosure date with you, fix the
issue on `main`, and publish a GitHub Security Advisory crediting you (unless
you prefer to stay anonymous). Where it is warranted, a CVE is requested
through GitHub. By default the advisory is published when the fix is on
`main`, or 90 days after your report, whichever comes first.

**If the report is declined:** you get an explanation. If the issue is a
missing safeguard rather than a vulnerability, we may track it as a public
hardening issue; we will ask you before quoting your report.

## Scope

### In scope

- **Safety interlock bypass:** transmitting on an interface that is not in
  `/etc/bngload/interfaces.allow`, without the lab marker
  `/etc/bngload/lab-host`, or on a port that carries an IPv4 or global IPv6
  address (design SR-11, SR-19).
- **Rate-limit bypass:** making `tstamp check` exceed 1000 frames/s, making
  `afprobe` exceed its TX-only cap or send `max` without `--rx-if`, or
  continuing after its "ports not cabled back-to-back" stop (design SR-14).
- **Local privilege escalation:** arbitrary file write, read or execution
  through the root-run tools or scripts — temporary files, symlinks, path
  handling, environment variables, the installed `bngload-lab` wrapper, or
  the systemd unit and helper that `host-prep.sh --persist` installs.
- **Memory safety:** bugs in the Go or Rust code that uses `unsafe` (ioctl
  structures, the TPACKET_V3 ring walk, `sendmmsg` buffers).
- **Untrusted input:** crashes, hangs or code execution caused by frames
  received on a test port, replies on the BNG Blaster control socket,
  netlink replies, or malformed lab specs, report files or the open-question
  register.
- **Supply chain:** install scripts that skip a verification they could
  perform, weaknesses in the CI workflow, or problems in a dependency
  (`libc` for Rust, `tomli` on Python 3.10, test-only tools).
- **Secret leakage:** credentials from the host or from configuration
  appearing in reports or logs beyond what is documented below.

### Out of scope

- Load sent to a DUT or AAA server that the operator configured and was
  authorised to test. Generating load is what the tool is for; exceeding the
  configured limits is in scope (see above).
- Attacks that already need root on the generator host, or write access to
  `/etc/bngload/`, to the root process's environment (for example
  `BNGLOAD_*` overrides passed with `sudo -E`), or to the repository
  checkout the operator runs from.
- Vulnerabilities in BNG Blaster, accel-ppp, FreeRADIUS, the Linux kernel or
  NIC firmware. Report those to their maintainers. We still want to hear if
  the way bngload uses them makes an issue worse.
- The placeholder secrets (`testing123`, `test`) and documentation addresses
  (192.0.2.0/24, 100.64.0.0/14, `fc00::/7` examples) in `lab/`.
- Missing safeguards that are documented below as planned work, unless you
  can show they make an in-scope attack possible. Suggestions are welcome as
  regular issues.

## Security model

- **Where it runs.** On a dedicated lab generator host, as root:
  `CAP_NET_RAW` and `CAP_NET_ADMIN` for raw sockets and NIC settings,
  `CAP_SYS_ADMIN` for the network-namespace benchmark. The test ports connect
  only to lab DUTs on segments with no route to production (design SR-23).
- **Trust boundaries.** Frames received on test ports, BNG Blaster
  control-socket replies and netlink replies are untrusted. Lab specs, the
  open-question register and run directories are operator input, but they
  are still parsed defensively, and every lab-spec problem is reported before
  anything runs.
- **Defences in place.**
  - Every transmitting tool checks the lab marker, the interface allowlist
    and the no-IP rule first, and refuses (fails closed) if a check cannot
    be evaluated. No flag disables the check.
  - Transmit rates are bounded. `tstamp check` sends at most 1000
    frames/s. `afprobe` TX-only runs are capped at 20 kpps unless the cap is
    raised explicitly, and back-to-back runs stop if the two ports are not
    looped. BNG Blaster runs use the rates in the operator's lab spec.
  - `host-prep.sh` is a dry run unless `--apply` is given, and it never adds
    or removes IP addresses.
  - Report files are written through exclusively created temporary files,
    which never follow a symlink.
  - Run-time dependencies are minimal: Go standard library, Rust `libc`
    pinned by `Cargo.lock`, Python standard library.
  - The Go installer checks the tarball's SHA-256 against go.dev.

## Known limitations (Phase 0)

These are accepted for Phase 0 and planned for later phases:

- **No privilege separation yet.** The tools run fully as root; dropping
  capabilities after setup and a separate namespace helper come with the
  engine (design SR-16).
- **No audit log** of who ran what with which overrides (design SR-21).
- **The BNG Blaster `.deb` is not checksum-verified,** because upstream
  publishes no checksums. It is downloaded over HTTPS from the official
  GitHub release, and the installer prints its SHA-256 for the record. Verify
  it yourself if your policy requires.
- **Lab credentials in run files.** The PPP username and password from a lab
  spec are written into the generated BNG Blaster configurations under
  `runs/…/blaster/`. Use lab-only credentials, never real subscriber ones,
  and keep run directories readable only by the operators who need them.
- **No SBOM, signed binaries or container images yet** (design SR-24).
- `scripts/install-deps-ubuntu.sh` installs Rust with the official
  `rustup` installer from `sh.rustup.rs`, over HTTPS without a pinned
  checksum. Install Rust yourself and pass `--no-rust` if your policy
  forbids that.

## Operator checklist

- Create `/etc/bngload/lab-host` only on authorised lab generator hosts.
- Keep `/etc/bngload/`, the repository checkout and every output directory
  (`runs/`, `hosts/`, anything passed with `-o` or `--out-dir`) owned by root
  and not writable by other users.
- Keep test and AAA segments isolated from production, with management on a
  separate interface (design SR-23).
- Strip secrets from DUT configurations before committing them to `lab/dut/`.

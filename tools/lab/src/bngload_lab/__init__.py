"""bngload Phase 0 lab harness.

The harness drives BNG Blaster (the incumbent open-source generator) to record
the Phase 0 baseline numbers for every DUT, keeps the design's open questions
tracked with owners, and turns the JSON reports written by every Phase 0 tool
(``bngload``, ``afprobe`` and this package) into the Phase 0 acceptance report.

It is written in Python because none of this is on a timing-critical path: it
orchestrates processes, parses JSON/TOML and does statistics on a few hundred
numbers (ADR-0001). The runtime uses the standard library only.

Modules
-------
``labspec``         TOML lab specification (DUT, access port, PPP, scenarios)
``blaster_config``  lab spec -> BNG Blaster JSON configuration
``blaster_run``     BNG Blaster process runner and control-socket client
``blaster_report``  parser for BNG Blaster's JSON report
``scenarios``       TS-01 and TS-04 equivalents and the N-session baseline
``stats``           percentiles, bootstrap and Wilson intervals
``openq``           open-question register checks
``acceptance``      Phase 0 acceptance report and host-profile merge
``envelope``        the ``bngload.report/v1`` result envelope
``safety``          lab-host and interface interlocks (same rules as Go/Rust)
"""

__version__ = "0.1.0"

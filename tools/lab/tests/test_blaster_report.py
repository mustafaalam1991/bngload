import json

from bngload_lab import blaster_report


def test_report_with_session_list(fixtures):
    r = blaster_report.load(fixtures / "blaster-report-sessions.json")
    assert r.version == "0.9.30-src"
    assert (r.sessions, r.established) == (3, 3)
    assert r.session_states == {"Terminated": 3}
    assert r.setup_time_ms == 302 and r.success_ratio == 1.0
    assert r.retransmissions == 0
    assert r.control_tx == 3 + 3 + 9 + 3 + 12 + 6 + 3


def test_report_with_session_count(fixtures):
    r = blaster_report.load(fixtures / "blaster-report-count.json")
    assert r.sessions == 20 and r.established == 20
    assert r.session_states == {}
    assert r.setup_rate_cps > 0


def test_partial_establishment_drops_setup_time(fixtures):
    doc = json.loads((fixtures / "blaster-report-count.json").read_text())
    doc["report"]["sessions-established"] = 19
    r = blaster_report.parse(doc)
    assert r.setup_time_ms is None and r.setup_rate_cps is None
    assert r.success_ratio == 0.95


def test_retransmissions_from_counters():
    r = blaster_report.parse(
        {
            "report": {
                "sessions": 10,
                "sessions-established": 10,
                "access-interfaces": [
                    {
                        "protocol-stats": {
                            "tx-padi": 13,
                            "rx-pado": 10,
                            "tx-padr": 11,
                            "tx-lcp": 30,
                            "lcp-request-timeout": 2,
                            "pap-timeout": 1,
                            "lcp-echo-timeout": 5,
                        }
                    }
                ],
            }
        }
    )
    # 3 extra PADI + 1 extra PADR + 2 LCP + 1 PAP; echo timeouts are not retransmissions
    assert r.retransmissions == 7
    assert abs(r.retransmission_ratio - 7 / 54) < 1e-9

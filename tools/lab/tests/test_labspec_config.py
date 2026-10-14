import pytest

from bngload_lab import blaster_config, labspec


def test_example_specs_in_repo_are_valid(repo):
    for name in ("lab.example.toml", "selfloop-veth.toml"):
        spec = labspec.load(repo / "lab" / "blaster" / name)
        assert spec.dut.name


def test_minimal_spec_gets_defaults(tmp_path, minimal_spec_toml):
    p = tmp_path / "s.toml"
    p.write_text(minimal_spec_toml)
    s = labspec.load(p)
    assert s.ppp.auth == "pap" and s.ts04.step_s == 30.0 and not s.is_selfloop
    assert s.interfaces() == ["p0"]


def test_every_problem_is_reported_at_once():
    with pytest.raises(labspec.SpecError) as e:
        labspec.parse(
            {
                "dut": {"mode": "loop", "colour": "red"},
                "access": {"outer_vlan": [5000, 1], "inner_vlan": [1, 2]},
                "ppp": {"auth": "eap", "mru": "big"},
                "ts04": {"min_rate": 100, "max_rate": 10},
                "extra": {},
            }
        )
    text = "\n".join(e.value.problems)
    for needle in (
        "dut.name: required",
        "dut.mode",
        "dut.colour: unknown key",
        "access.interface: required",
        "access.outer_vlan",
        "ppp.auth",
        "ppp.mru: expected int",
        "ts04: need 1 <= min_rate < max_rate",
        "extra: unknown section",
    ):
        assert needle in text, needle


def test_selfloop_needs_second_interface():
    with pytest.raises(labspec.SpecError, match="selfloop.interface"):
        labspec.parse({"dut": {"name": "x", "mode": "selfloop"}, "access": {"interface": "p0"}})


def test_render_qinq_selfloop(repo):
    spec = labspec.load(repo / "lab" / "blaster" / "selfloop-veth.toml")
    cfg = blaster_config.render(spec, sessions=50, rate=500)
    acc = cfg["interfaces"]["access"][0]
    assert acc == {
        "interface": "blt1",
        "type": "pppoe",
        "outer-vlan-min": 100,
        "outer-vlan-max": 199,
        "inner-vlan-min": 1,
        "inner-vlan-max": 4000,
    }
    assert cfg["interfaces"]["a10nsp"] == [{"interface": "blt0"}]
    assert cfg["sessions"]["max-outstanding"] == 1000  # raised to 2x rate
    assert cfg["ppp"]["authentication"]["protocol"] == "PAP"
    assert "session-time" not in cfg["pppoe"]


def test_render_untagged_external(tmp_path, minimal_spec_toml):
    p = tmp_path / "s.toml"
    p.write_text(minimal_spec_toml + '[ppp]\nauth = "chap"\ndhcpv6_pd = true\n')
    cfg = blaster_config.render(labspec.load(p), 1, 1, session_time_s=9.6)
    assert cfg["interfaces"] == {"access": [{"interface": "p0", "type": "pppoe"}]}
    assert cfg["ppp"]["authentication"]["protocol"] == "CHAP"
    assert cfg["dhcpv6"] == {"enable": True}
    assert cfg["pppoe"]["session-time"] == 10
    with pytest.raises(ValueError):
        blaster_config.render(labspec.load(p), 0, 1)

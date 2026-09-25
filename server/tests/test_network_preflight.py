from app.network_preflight import (
    NetworkPreflightError,
    is_loopback_url,
    preflight,
    reject_insecure_remote,
    reject_loopback,
)


def test_loopback_rejected():
    for url in (
        "http://127.0.0.1:8080",
        "http://localhost:8081",
        "https://127.0.1.4/health",
        "http://[::1]:8080",
    ):
        assert is_loopback_url(url)
        try:
            reject_loopback(url, role="Control plane")
            raise AssertionError(url)
        except NetworkPreflightError:
            pass
    assert not is_loopback_url("http://stowline-cp.hq.local:8080")
    assert not is_loopback_url("http://192.168.10.20:8081")


def test_preflight_requires_remote_urls_and_forbids_localhost_flag():
    out = preflight({"control_plane_url": "", "gateway_url": "", "allow_localhost": False})
    assert out["ok"] is False
    assert out["can_enroll"] is False
    assert out["google_backend"] == "unavailable"
    try:
        preflight({"control_plane_url": "http://10.0.0.5:8080", "gateway_url": "http://10.0.0.5:8081", "allow_localhost": True})
        raise AssertionError("allow_localhost must fail")
    except NetworkPreflightError:
        pass
    blocked = preflight(
        {"control_plane_url": "http://127.0.0.1:8080", "gateway_url": "http://localhost:8081", "allow_localhost": False}
    )
    assert blocked["ok"] is False
    assert any("localhost" in e.lower() or "127.0.0.1" in e for e in blocked["errors"])


def test_remote_http_rejected_unless_lab():
    try:
        reject_insecure_remote("http://192.168.10.20:8080", role="Control plane")
        raise AssertionError("remote HTTP must fail")
    except NetworkPreflightError as exc:
        assert "https" in str(exc).lower()
    reject_insecure_remote(
        "http://192.168.10.20:8080", role="Control plane", lab_insecure_http=True
    )
    reject_insecure_remote("https://stowline-control.hq.local", role="Control plane")
    try:
        reject_insecure_remote("https://127.0.0.1", role="Control plane")
        raise AssertionError("loopback HTTPS must fail")
    except NetworkPreflightError:
        pass
    blocked = preflight(
        {
            "control_plane_url": "http://192.168.10.20:8080",
            "gateway_url": "http://192.168.10.20:8081",
            "allow_localhost": False,
        }
    )
    assert blocked["ok"] is False
    assert blocked["can_enroll"] is False
    assert any("https" in e.lower() for e in blocked["errors"])

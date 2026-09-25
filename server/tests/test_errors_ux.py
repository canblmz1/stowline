from app.errors_ux import catalog, explain_error


def test_error_catalog_covers_required_cases():
    codes = {i["code"] for i in catalog()["items"]}
    for need in (
        "PC_OFFLINE",
        "SERVICE_STOPPED",
        "GATEWAY_UNAVAILABLE",
        "CONTROL_PLANE_UNAVAILABLE",
        "NO_WAN_ADMISSION",
        "WINDOW_MISSED",
        "DISK",
        "VSS",
        "PST_LOCKED",
        "AUTH",
        "PROVIDER",
        "CANCELLED",
    ):
        assert need in codes
    human = explain_error("VSS")
    assert human["title"]
    assert "technical" in human
    assert explain_error("SITE_UNMEASURED")["code"] == "NO_WAN_ADMISSION"

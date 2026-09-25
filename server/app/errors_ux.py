"""Operator-facing error catalog. Technical details stay expandable."""

from __future__ import annotations

from datetime import datetime, timezone
from zoneinfo import ZoneInfo

ERROR_CATALOG = {
    "PC_OFFLINE": {
        "title": "Bilgisayar çevrimdışı",
        "summary": "Bu uç nokta son zamanlarda bağlantı kurmadı. Bilgisayar açılıp Stowline servisi çalışmadan yedekleme başlayamaz veya sürmez.",
    },
    "SERVICE_STOPPED": {
        "title": "Yedekleme servisi durdurulmuş",
        "summary": "StowlineBackup kurulu ama çalışmıyor. Servisi Windows Hizmetleri'nden başlatın. restic'i elle çalıştırmayın.",
    },
    "GATEWAY_UNAVAILABLE": {
        "title": "Yedekleme ağ geçidine ulaşılamıyor",
        "summary": "Kontrol düzlemi cihazı depolama ağ geçidine kaydedemiyor ya da ağ geçidi yanıt vermiyor. Bulut yedekleme başlatılamaz.",
    },
    "CONTROL_PLANE_UNAVAILABLE": {
        "title": "Kontrol düzlemine ulaşılamıyor",
        "summary": "PC, Stowline kontrol düzlemine bağlanamıyor. Yerel zamanlama daha sonra çalışabilir; kayıt işlemleri ve canlı komutlar bağlantı gelene kadar bekler.",
    },
    "NO_WAN_ADMISSION": {
        "title": "Ağ (WAN) izni yok",
        "summary": "Sitenin ölçülmüş kapasitesi yok, izinler duraklatılmış ya da tek WAN yuvasını başka bir yedekleme tutuyor. Sıfır hiçbir zaman sınırsız sayılmaz.",
    },
    "WINDOW_MISSED": {
        "title": "Yedekleme penceresi kaçırıldı",
        "summary": "PC kapalıydı ya da izin verilen en geç başlangıçtan sonra uygun değildi. Kaçırılan çalışmalar bir sonraki sınırlı telafide birleştirilir. İkinci bir eş zamanlı WAN yedeklemesi başlatmayın.",
    },
    "DISK": {
        "title": "Disk sorunu",
        "summary": "PC'de yeterli boş alan yok ya da bir kaynak sürücü eksik. Yedeklemeden önce boş alan ve kaynak yolları sağlıklı olmalı.",
    },
    "VSS": {
        "title": "VSS anlık görüntüsü alınamadı",
        "summary": "Windows Birim Gölge Kopyası kullanılabilir bir anlık görüntü üretmedi. PST gibi kilitli dosyalar için başarılı bir VSS anlık görüntüsü gerekir.",
    },
    "PST_LOCKED": {
        "title": "Outlook PST dosyası kilitli",
        "summary": "Outlook posta dosyasını açık tutuyor ve VSS tutarlı bir kopya sağlayamadı. Outlook'u açık bırakın; gölge kopyayı servis almalı.",
    },
    "AUTH": {
        "title": "Kimlik doğrulama başarısız",
        "summary": "Cihaz kimlik bilgileri reddedildi. Yalnızca yeni bir tek kullanımlık token ile yeniden kaydedin. Sırları sohbete ya da bilete yapıştırmayın.",
    },
    "PROVIDER": {
        "title": "Bulut veya ağ geçidi hatası",
        "summary": "Depolama sağlayıcısı ya da ağ geçidi hata döndürdü. İş güvenli şekilde başarısız sayılır. prune, forget, unlock ya da repair çalıştırmayın.",
    },
    "CANCELLED": {
        "title": "Yedekleme iptal edildi",
        "summary": (
            "Çalışan iş durduruldu; nedeni kaydedilmedi (bir operatör iptali ya da yedekleme penceresinin dolması olabilir). "
            "İptal başarı sayılmaz ve yeni bir yedek olarak sayılmaz. O ana kadar yüklenen veri korunur; bir sonraki çalışma kaldığı yerden sürer."
        ),
    },
    "FAILED": {
        "title": "Yedekleme başarısız",
        "summary": "Son yedekleme denemesi başarılı olmadı. Kesin hata sınıfı için teknik ayrıntıyı açın.",
    },
}

CLASS_MAP = {
    "NETWORK": "CONTROL_PLANE_UNAVAILABLE",
    "PROVIDER_5XX": "PROVIDER",
    "PROVIDER_RATE_LIMIT": "PROVIDER",
    "PROVIDER_QUOTA": "PROVIDER",
    "AUTH": "AUTH",
    "VSS": "VSS",
    "CONSISTENCY_NOT_MET": "VSS",
    "DISK": "DISK",
    "NOSPACE": "DISK",
    "CANCELLED": "CANCELLED",
    "WINDOW": "WINDOW_MISSED",
    "ADMISSION": "NO_WAN_ADMISSION",
    "SITE_UNMEASURED": "NO_WAN_ADMISSION",
    "PAUSED": "NO_WAN_ADMISSION",
    "PST": "PST_LOCKED",
}

# The agent reports a cancelled run with the same class whoever or whatever
# caused it, and never says why. Two causes leave evidence the control plane
# can check: an operator cancel command that was acked right before the run
# ended, and a run that ended at the schedule's hard stop.
_STOP_EARLY_TOLERANCE_SECONDS = 120  # clock skew between agent and control plane
_STOP_LATE_TOLERANCE_SECONDS = 900  # the agent reports on its next poll, which can lag
_OPERATOR_LOOKBACK_SECONDS = 900  # a queued command lives 15 minutes


def _aware(dt: datetime) -> datetime:
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=timezone.utc)


def infer_cancel_cause(ended_at: datetime | None, schedule: dict | None, operator_cancels) -> str:
    """"operator", "window", or "" when the evidence does not say."""
    if ended_at is None:
        return ""
    ended = _aware(ended_at)
    for acked in operator_cancels or ():
        if acked is not None and -60 <= (ended - _aware(acked)).total_seconds() <= _OPERATOR_LOOKBACK_SECONDS:
            return "operator"
    sched = schedule or {}
    hard, tzname = str(sched.get("hard_stop") or ""), str(sched.get("timezone") or "")
    if hard and tzname:
        from app.schedule_policy import parse_hhmm

        h, m = parse_hhmm(hard)
        local = ended.astimezone(ZoneInfo(tzname))
        delta = (local - local.replace(hour=h, minute=m, second=0, microsecond=0)).total_seconds()
        if -_STOP_EARLY_TOLERANCE_SECONDS <= delta <= _STOP_LATE_TOLERANCE_SECONDS:
            return "window"
    return ""


def _cancelled_info(cancel_cause: str, hard_stop: str) -> dict:
    if cancel_cause == "operator":
        return {
            "title": "Yedekleme iptal edildi",
            "summary": (
                "Bir operatör çalışan işi iptal etti. İptal başarı sayılmaz ve yeni bir yedek olarak sayılmaz. "
                "O ana kadar yüklenen veri korunur; bir sonraki çalışma kaldığı yerden sürer."
            ),
        }
    if cancel_cause == "window" and hard_stop:
        return {
            "title": "Yedekleme penceresi doldu",
            "summary": (
                f"Yedekleme, izin verilen sürenin sonunda ({hard_stop}) ajan tarafından durduruldu; bunu bir operatör yapmadı. "
                "Bu başarı sayılmaz ve yeni bir yedek olarak sayılmaz. Yüklenen veri korunur; bir sonraki çalışma "
                "kaldığı yerden sürer (yüklenmiş veri büyük ölçüde tekrar yüklenmez)."
            ),
        }
    return ERROR_CATALOG["CANCELLED"]


def explain_error(
    error_class: str | None,
    *,
    offline: bool = False,
    service_stopped: bool = False,
    wan_ready: bool | None = None,
    cancel_cause: str = "",
    hard_stop: str = "",
) -> dict:
    if offline:
        code = "PC_OFFLINE"
    elif service_stopped:
        code = "SERVICE_STOPPED"
    elif wan_ready is False:
        code = "GATEWAY_UNAVAILABLE"
    else:
        raw = (error_class or "").upper()
        code = CLASS_MAP.get(raw, raw if raw in ERROR_CATALOG else ("FAILED" if raw else ""))
    if not code:
        return {"code": "", "title": "", "summary": "", "technical": error_class or ""}
    info = _cancelled_info(cancel_cause, hard_stop) if code == "CANCELLED" else ERROR_CATALOG[code]
    return {"code": code, "title": info["title"], "summary": info["summary"], "technical": error_class or ""}


def catalog() -> dict:
    return {"schema_version": 1, "items": [{"code": k, **v} for k, v in ERROR_CATALOG.items()]}

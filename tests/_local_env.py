"""Shared local gateway config & model discovery for tests/*.py."""

import json
import os
import pathlib
import urllib.request

_REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent


def _load_dotenv_local() -> dict[str, str]:
    path = _REPO_ROOT / ".env.local"
    if not path.is_file():
        return {}
    out: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        k = k.strip()
        v = v.strip()
        if len(v) >= 2 and v[0] == v[-1] and v[0] in ("'", '"'):
            v = v[1:-1]
        if k:
            out[k] = v
    return out


_DOTENV = _load_dotenv_local()


def get_env(name: str, default: str = "") -> str:
    val = os.environ.get(name)
    if val:
        return val
    val = _DOTENV.get(name)
    if val:
        return val
    return default


def get_base_url() -> str:
    return get_env("CPA_BASE_URL", "http://127.0.0.1:8317").rstrip("/")


def get_api_key() -> str:
    key = get_env("CPA_API_KEY")
    if not key:
        raise SystemExit(
            "CPA_API_KEY is not set. Copy .env.example to .env.local or set CPA_API_KEY."
        )
    return key


def discover_models(base_url: str, api_key: str) -> list[str]:
    req = urllib.request.Request(
        f"{base_url}/v1/models",
        headers={"Authorization": f"Bearer {api_key}"},
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        return [
            m["id"]
            for m in data.get("data", [])
            if isinstance(m, dict) and isinstance(m.get("id"), str)
        ]
    except Exception:
        return []


def resolve_protected_model(base_url: str, api_key: str) -> str:
    explicit = get_env("CPA_MODEL")
    if explicit:
        return explicit
    models = discover_models(base_url, api_key)
    for m in models:
        if m.startswith("agy/gemini-") and not m.endswith("-image"):
            return m
    for m in models:
        if m.startswith("agy/") and not m.endswith("-image"):
            return m
    return "agy/gemini-4-hydrogen"


def resolve_bypass_model(base_url: str, api_key: str, protected_model: str) -> str:
    explicit = get_env("CPA_BYPASS_MODEL")
    if explicit:
        return explicit
    models = discover_models(base_url, api_key)
    candidate = protected_model.removeprefix("agy/")
    if candidate in models:
        return candidate
    for m in models:
        if not m.startswith("agy/") and not m.endswith("-image"):
            return m
    return "gemini-4-hydrogen"

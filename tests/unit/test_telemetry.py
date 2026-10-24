import pytest

from mgar_agents.core.telemetry import TraceContext, safe_attributes


def test_trace_context_has_no_auth_or_baggage():
    c = TraceContext.for_agent("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
    assert set(c.headers()) == {"traceparent"}
    assert len(c.traceparent) == 55
    with pytest.raises(ValueError):
        TraceContext(c.traceparent + " PRIVATE_SYNTHETIC_CONTENT")
    with pytest.raises(ValueError):
        TraceContext("00-" + "0" * 32 + "-" + "1" * 16 + "-01")


def test_telemetry_closed_attributes_and_unknown_usage():
    attrs = safe_attributes("chat", "research")
    assert not attrs["mgar.usage.known"]
    assert "gen_ai.usage.input_tokens" not in attrs
    assert safe_attributes("chat", "research", known=True, input_tokens=3)["gen_ai.usage.input_tokens"] == 3
    for kwargs in [{"known": False, "input_tokens": 0}, {"known": True, "input_tokens": -1}, {"known": True, "input_tokens": True}]:
        with pytest.raises(ValueError):
            safe_attributes("chat", "research", **kwargs)
    with pytest.raises(ValueError):
        safe_attributes("PRIVATE_SYNTHETIC_CONTENT", "research")

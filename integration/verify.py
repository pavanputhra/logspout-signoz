import json, sys

def anyval(v):
    if not isinstance(v, dict): return v
    for k in ("stringValue","boolValue","doubleValue"):
        if k in v: return v[k]
    if "intValue" in v: return int(v["intValue"])
    if "arrayValue" in v: return [anyval(x) for x in v["arrayValue"].get("values",[])]
    if "kvlistValue" in v: return {kv["key"]: anyval(kv["value"]) for kv in v["kvlistValue"].get("values",[])}
    return None

def attrs(lst): return {kv["key"]: anyval(kv["value"]) for kv in (lst or [])}

def load(path):
    out = []
    for line in open(path):
        line = line.strip()
        if not line: continue
        doc = json.loads(line)
        for rl in doc.get("resourceLogs", []):
            res = attrs(rl.get("resource", {}).get("attributes"))
            for sl in rl.get("scopeLogs", []):
                for r in sl.get("logRecords", []):
                    out.append({
                        "body": anyval(r.get("body")),
                        "sev": r.get("severityText"),
                        "sevnum": r.get("severityNumber"),
                        "ts": r.get("timeUnixNano"),
                        "observed": r.get("observedTimeUnixNano"),
                        "trace": r.get("traceId"),
                        "span": r.get("spanId"),
                        "attrs": attrs(r.get("attributes")),
                        "res": res,
                        "scope": sl.get("scope", {}).get("name"),
                    })
    return out

path, label = sys.argv[1], sys.argv[2]
transport = sys.argv[3] if len(sys.argv) > 3 else "otlp"

# SigNoz's httplogreceiver decodes every JSON number as float64 and stringifies
# nested values, so these cannot survive that path regardless of what we send.
# The OTLP payload carries them exactly; verified separately.
RECEIVER_LIMITS = {
    "signoz": {"pino big int exact", "typed attrs: nested map", "typed attrs: array"},
    "otlp": set(),
}
recs = load(path)
by_body = {}
for r in recs:
    by_body.setdefault(str(r["body"])[:60], r)

print(f"===== {label}: {len(recs)} records =====")

def check(name, cond, detail=""):
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + (f"   {detail}" if (detail and not cond) else ""))
    return cond

fails = 0
def find(sub):
    for b, r in by_body.items():
        if sub in b: return r
    return None

cases = [
  ("plain line collected",            lambda: find("plain unstructured") is not None, ""),
  ("[ERROR] -> error",                lambda: find("bracketed level")["sev"] == "error", ""),
  ("logfmt level=warn -> warn",       lambda: find("logfmt style")["sev"] == "warn", ""),
  ("no false level match",            lambda: find("terrorist")["sev"] == "info", ""),
  ("winston message+timestamp",       lambda: find("winston style")["sev"]=="info" and find("winston style")["attrs"].get("userId")==7, ""),
  ("pino numeric 40 -> warn",         lambda: find("pino style")["sev"] == "warn", ""),
  ("pino big int exact",              lambda: find("pino style")["attrs"].get("order_id") == 9007199254740993, ""),
  ("bunyan numeric 50 -> error",      lambda: find("bunyan style")["sev"] == "error", ""),
  ("zap float ts -> error",           lambda: find("zap style")["sev"] == "error", ""),
  ("logrus warning -> warn",          lambda: find("logrus style")["sev"] == "warn", ""),
  ("docker 'log' key as body",        lambda: find("docker json-file") is not None, ""),
  ("trace_id preserved",              lambda: find("trace correlated")["trace"] == "000000000000000018c51935df0b93b9", ""),
  ("span_id preserved",               lambda: find("trace correlated")["span"] == "18c51935df0b93b9", ""),
  ("typed attrs: int",                lambda: find("typed attrs")["attrs"].get("count") == 42, ""),
  ("typed attrs: double",             lambda: find("typed attrs")["attrs"].get("ratio") == 1.5, ""),
  ("typed attrs: bool",               lambda: find("typed attrs")["attrs"].get("ok") is True, ""),
  ("typed attrs: nested map",         lambda: isinstance(find("typed attrs")["attrs"].get("tags"), dict), ""),
  ("typed attrs: array",              lambda: isinstance(find("typed attrs")["attrs"].get("list"), list), ""),
  ("service override from json",      lambda: find("service override")["res"].get("service.name") == "billing-svc", ""),
  ("env override from json",          lambda: find("service override")["res"].get("deployment.environment") == "staging", ""),
  ("bad trace id -> attribute",       lambda: find("bad trace id")["trace"] in (None,"") and find("bad trace id")["attrs"].get("trace_id")=="not-hex-at-all", ""),
  ("no level -> info default",        lambda: find("no level field")["sev"] == "info", ""),
  ("non-object json level detected",  lambda: find("non-object json")["sev"] == "error", ""),
  ("unicode/emoji intact",            lambda: "🚀" in find("unicode")["body"], ""),
  ("4000-char line intact",           lambda: len([r for r in recs if isinstance(r["body"],str) and len(r["body"])>=4000]) > 0, ""),
  ("FATAL -> fatal",                  lambda: find("something exploded")["sev"] == "fatal", ""),
  ("stderr collected",                lambda: find("stderr diagnostic") is not None, ""),
  ("stderr marked as stderr",         lambda: find("stderr diagnostic")["attrs"].get("log.iostream") == "stderr", ""),
  ("env from route",                  lambda: find("plain unstructured")["res"].get("deployment.environment") == "itest", ""),
  ("service.name from compose label", lambda: find("plain unstructured")["res"].get("service.name") == "producer", ""),
  ("container.name attribute",        lambda: "producer" in str(find("plain unstructured")["attrs"].get("container.name")), ""),
  ("UNLABELLED CONTAINER EXCLUDED",   lambda: find("MUST-NOT-BE-COLLECTED") is None, ""),
  ("no logspout self-logs",           lambda: find("routing to") is None, ""),
]
limits = RECEIVER_LIMITS.get(transport, set())
skipped = 0
for name, fn, _ in cases:
    try:
        ok = fn()
    except Exception:
        ok = False
    if name in limits:
        print(f"  {'n/a ' if not ok else 'PASS'}  {name}" + ("   (receiver limitation, not the adapter)" if not ok else ""))
        skipped += 1
        continue
    if not check(name, ok): fails += 1

total = len(cases) - skipped
print(f"  ----> {total-fails}/{total} passed" + (f", {skipped} n/a on this receiver" if skipped else ""))
sys.exit(1 if fails else 0)

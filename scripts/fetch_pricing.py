#!/usr/bin/env python3
"""Fetch unit prices from the AWS Price List API (needs pricing:GetProducts) -> lab/pricing.json. Region ap-northeast-1 (Tokyo).
Unit prices for cost estimates (docs/dynamodb-fit.md). Re-run to refresh; the file records the fetch date."""
import datetime, json, pathlib, subprocess, sys

root = pathlib.Path(__file__).resolve().parent.parent
LOC = "Asia Pacific (Tokyo)"


def products(service, filters):
    f = [f"Type=TERM_MATCH,Field={k},Value={v}" for k, v in filters.items()]     # ONE --filters flag with all filters (a repeated flag overrides)
    r = subprocess.run(["aws", "pricing", "get-products", "--region", "us-east-1", "--service-code", service, "--filters", *f, "--max-results", "100", "--output", "json"],
                       capture_output=True, text=True)
    if r.returncode: raise RuntimeError(r.stderr[-300:])
    return [json.loads(x) for x in json.loads(r.stdout)["PriceList"]]


def usd(p):
    for t in p["terms"]["OnDemand"].values():
        for d in t["priceDimensions"].values():
            return float(d["pricePerUnit"]["USD"]), d["unit"], d["description"]


out = {"fetched": datetime.date.today().isoformat(), "region": "ap-northeast-1", "currency": "USD", "source": "AWS Price List API (on-demand, standard I/O Aurora)", "items": {}}
try:
    def one(service, usagetype, **kw):
        ps = products(service, {"location": LOC, "usagetype": usagetype, **kw})
        if not ps: raise RuntimeError(f"no price for {service} {usagetype}")
        return usd(ps[0])
    for cls, ut in (("db.r6g.large", "db.r6g.large"), ("db.r6g.xlarge", "db.r6g.xl")):      # the usage type abbreviates xlarge
        v, u, d = one("AmazonRDS", f"APN1-InstanceUsage:{ut}", databaseEngine="Aurora MySQL"); out["items"][f"aurora_instance_hourly[{cls}]"] = {"usd": v, "unit": u, "desc": d}
    ps = [x for x in products("AmazonRDS", {"usagetype": "APN1-Aurora:StorageIOUsage"}) if "Aurora MySQL" in json.dumps(x["terms"])]   # engine is only in the description
    v, u, d = usd(ps[0])
    out["items"]["aurora_io_per_million"] = {"usd": v * 1e6, "unit": "per 1M requests", "desc": d, "raw_unit": u}
    v, u, d = one("AmazonDynamoDB", "APN1-WriteRequestUnits"); out["items"]["ddb_wru_per_million"] = {"usd": v * 1e6, "unit": "per 1M write request units", "desc": d}
    v, u, d = one("AmazonDynamoDB", "APN1-ReadRequestUnits"); out["items"]["ddb_rru_per_million"] = {"usd": v * 1e6, "unit": "per 1M read request units", "desc": d}
except Exception as e:
    out["error"] = str(e)[:300]
(root / "lab/pricing.json").write_text(json.dumps(out, indent=1) + "\n")
print(json.dumps({k: v["usd"] for k, v in out["items"].items()}, indent=1), out.get("error", ""))

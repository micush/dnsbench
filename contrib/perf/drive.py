#!/usr/bin/env python3
"""Run one benchmark job through the dnsbench REST API and print the summary.

usage: drive.py BASE_URL USER PASSWORD SERVER CONCURRENCY PIPELINE DURATION [JSON_EXTRA]

Example (see reflect.c):
  drive.py http://127.0.0.1:8453 benchuser 'secret' 127.0.0.1:5399 2 64 10s

To compare CPU cost per query, read utime+stime (fields 14 and 15 of
/proc/<pid>/stat, in 1/100 s) of the dnsbench process before and after a run
and divide by the number of queries sent.
"""
import json
import sys
import urllib.request

if len(sys.argv) < 8:
    sys.exit(__doc__)
base, user, pw, server, conc, pipe, dur = sys.argv[1:8]
extra = json.loads(sys.argv[8]) if len(sys.argv) > 8 else {}


def post(path, body, tok=None):
    headers = {"Content-Type": "application/json"}
    if tok:
        headers["X-Session-Token"] = tok
    req = urllib.request.Request(base + path, json.dumps(body).encode(), headers)
    return json.load(urllib.request.urlopen(req, timeout=30))


tok = post("/api/login", {"username": user, "password": pw})["token"]
args = {"server": server, "protocol": "udp", "concurrency": conc, "pipeline": pipe,
        "duration": dur, "queries": "a.example\nb.example\nc.example", "query_type": "A"}
args.update(extra)
job = post("/api/start-job", args, tok)["job_id"]
req = urllib.request.Request(base + "/api/job/%s/output" % job, headers={"X-Session-Token": tok})
out = urllib.request.urlopen(req, timeout=3600).read().decode()
for line in out.splitlines():
    if (" q/s" in line and "[" not in line) or " sent " in line or "Latency" in line or "RCODE" in line:
        print(line)

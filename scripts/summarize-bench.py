#!/usr/bin/env python3
"""Combine benchmark JSON and process samples from benchmark-suite.sh."""

import argparse
import csv
import json
from collections import defaultdict
from pathlib import Path


def resource_summary(samples):
    snapshots = sorted(samples.items())
    memory = []
    cpu_rates = []
    for _, processes in snapshots:
        sizes = [sample[2] for sample in processes.values()]
        if sizes and None not in sizes:
            memory.append(sum(sizes) / 1024)
    for (start, before), (end, after) in zip(snapshots, snapshots[1:]):
        common = before.keys() & after.keys()
        if common:
            cpu_seconds = sum((after[pid][0] - before[pid][0]) / after[pid][1] for pid in common)
            cpu_rates.append(cpu_seconds / ((end - start) / 1e9))
    return {
        "snapshots": len(snapshots),
        "peak_pss_mib": round(max(memory), 2) if memory else None,
        "peak_sample_cpu_cores_common_processes": round(max(cpu_rates), 3) if cpu_rates else None,
        "cpu_intervals": len(cpu_rates),
    }


def load_trial(path):
    result = json.loads(path.read_text())
    by_service = {"api": defaultdict(dict), "db": defaultdict(dict)}
    resource_path = path.with_name(path.stem + "-resources.csv")
    with resource_path.open() as source:
        for row in csv.DictReader(source):
            identity = (row["pid"], row["process_start"])
            pss = int(row["pss_kib"]) if row["pss_kib"] else None
            snapshot = by_service[row["service"]][int(row["timestamp_ns"])]
            snapshot[identity] = (int(row["cpu_ticks"]), int(row["clock_ticks_per_second"]), pss)
    result["resources"] = {service: resource_summary(samples) for service, samples in by_service.items()}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    paths = sorted(args.directory.glob("*.json"))
    if not paths:
        parser.error("no benchmark trial JSON found")
    results = {path.stem: load_trial(path) for path in paths}
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Validate the shipped Grafana dashboard against exported StePanel metrics."""

import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DASHBOARD = os.path.join(ROOT, "observability", "grafana-dashboard.json")


def main():
    with open(DASHBOARD, encoding="utf-8") as source:
        dashboard = json.load(source)

    panels = dashboard.get("panels")
    if not isinstance(panels, list) or not panels:
        raise ValueError("dashboard must contain a non-empty panels array")

    go_sources = []
    for directory, subdirectories, names in os.walk(ROOT):
        subdirectories[:] = [name for name in subdirectories if name not in {".git", "node_modules", "vendor", "__pycache__"}]
        for name in names:
            if name.endswith(".go") and not name.endswith("_test.go"):
                path = os.path.join(directory, name)
                with open(path, encoding="utf-8", errors="ignore") as source:
                    go_sources.append(source.read())
    source_text = "\n".join(go_sources)

    titles = set()
    metrics = set()
    rectangles = []
    for panel in panels:
        title = panel.get("title")
        if not isinstance(title, str) or not title.strip() or title in titles:
            raise ValueError(f"dashboard panel title is missing or duplicated: {title!r}")
        titles.add(title)

        grid = panel.get("gridPos", {})
        x, y, width, height = (grid.get(key) for key in ("x", "y", "w", "h"))
        valid_grid = all(isinstance(value, int) for value in (x, y, width, height))
        if not valid_grid or x < 0 or y < 0 or width < 1 or height < 1 or x + width > 24:
            raise ValueError(f"panel {title!r} has invalid grid geometry")
        for old_title, old_x, old_y, old_width, old_height in rectangles:
            intersects = x < old_x + old_width and old_x < x + width and y < old_y + old_height and old_y < y + height
            if intersects:
                raise ValueError(f"panels {title!r} and {old_title!r} overlap")
        rectangles.append((title, x, y, width, height))

        targets = panel.get("targets")
        if not isinstance(targets, list) or not targets:
            raise ValueError(f"panel {title!r} has no query targets")
        for target in targets:
            expression = target.get("expr")
            if not isinstance(expression, str) or not expression.strip():
                raise ValueError(f"panel {title!r} has an empty PromQL expression")
            if re.search(r"\b(?:site(?:_id)?|domain|account|customer|owner|path|user(?:name)?|tenant)\s*=", expression):
                raise ValueError(f"panel {title!r} uses a high-cardinality or sensitive label")
            metrics.update(re.findall(r"\b(stepanel_[a-zA-Z0-9_]+)", expression))

    missing = sorted(metric for metric in metrics if metric not in source_text)
    if missing:
        raise ValueError("dashboard references metrics not found in Go exporters: " + ", ".join(missing))
    print(f"Grafana dashboard valid: {len(panels)} non-overlapping panels, {len(metrics)} exported metrics")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"Grafana dashboard validation failed: {error}", file=sys.stderr)
        sys.exit(1)

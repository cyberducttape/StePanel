#!/usr/bin/env python3
"""Check that documentation only names things that exist.

For every current Markdown file (not docs/archive, not dated lab records, not
the changelog) this verifies that referenced repository files, Go tests,
Go functions written as `name()`, `stepanel` subcommands, `/api/...` routes,
and STEPANEL_* settings exist in the code. Fenced code blocks are skipped for
name checks because documents use them for labelled sketches; settings are
still checked there because install examples must use real variables.

Intentional references to planned or removed things are listed in ALLOWED
with the reason. Exit status is 1 when anything else is found.
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
os.chdir(ROOT)

SKIP_DIRS = ("docs/archive", "docs/lab-results", "node_modules", ".git", "test-results")
SKIP_FILES = {"CHANGELOG.md"}

# (document, reference) pairs that intentionally name something that does not
# exist on main. Keep the reason next to each entry.
ALLOWED = {
    ("VISION.md", "stepanel doctor"): "product vision: planned command",
    ("VISION.md", "stepanel site"): "product vision: planned command",
    ("REFACTORING_PLAN.md", "cmd/stepanel/main.go"): "refactoring target layout",
    ("REFACTORING_PLAN.md", "app.go"): "refactoring target layout",
    ("REFACTORING_PLAN.md", "init.go"): "refactoring plan history",
    ("ARCHITECTURE.md", "RegisterRoutes()"): "proposed package convention",
    ("CLAUDE.md", "internal/accounts/service.go"): "illustrative extraction example",
    ("CLAUDE.md", "internal/sites.Manager"): "type reference, not a file",
    ("docs/V1_PRODUCTION_GATES.md", "internal/operations/distributed_locks.go"): "documents its removal",
    ("docs/ASSET_MODEL.md", "STEPANEL_WEB_OVERRIDE_ROOT"): "described as a possible future option",
    ("docs/ASSET_MODEL.md", "web/static/newfile.js"): "example name",
    ("docs/DOCUMENTATION_GUIDE.md", "WORKFLOW_NAME.md"): "template placeholder",
    ("docs/CONTROLPLANE_LOAD.md", "BenchmarkControlPlane"): "prefix of the benchmark family",
    ("docs/ARCHIVE_IMPORTER.md", "getenv()"): "PHP function in a wp-config example",
    ("docs/ARCHIVE_IMPORTER.md", "env()"): "PHP function in a wp-config example",
    ("docs/ARCHIVE_IMPORTER.md", "define()"): "PHP function in a wp-config example",
    ("docs/ENCRYPTION_KEYS.md", "random()"): "names a weak key-generation method to avoid",
    ("docs/ENCRYPTION_KEYS.md", "rand()"): "names a weak key-generation method to avoid",
    ("docs/HELPER_LAYER_COMPLETION_SUMMARY.md", "internal/rootbroker/operations.go"): "historical; recorded as merged into broker.go",
    ("docs/HELPER_LAYER_COMPLETION_SUMMARY.md", "operations_test.go"): "historical; recorded as merged into broker_test.go",
    ("docs/PIP_INSTALL_CONSTRAINTS.md", "internal/helper/pip.go"): "unimplemented proposal, labelled as such",
    ("docs/PIP_INSTALL_CONSTRAINTS.md", "stepanel wheels"): "unimplemented proposal, labelled as such",
    ("docs/ROOT_HELPER_MODERNIZATION.md", "cmd/stepanel-helper/main.go"): "historical roadmap, labelled as such",
}
# Shell variables used in documented client examples, not panel settings.
CLIENT_VARIABLES = {"STEPANEL_TOKEN"}
# Words that follow "stepanel" in prose without being subcommands.
NOT_COMMANDS = {
    "service", "services", "account", "user", "group", "binary", "control", "root", "worker-service",
    "host", "is", "and", "the", "to", "for", "on", "in", "as", "with", "itself", "process",
    "repo", "repository", "release", "tarball", "panel", "daemon", "can", "will", "uses",
    "runs", "does", "must", "package", "stepanel-worker", "stepanel-root-broker", "namespace",
    "secrets", "create", "deploy", "image", "container", "chart", "pod", "deployment",
}


def git_files():
    return subprocess.check_output(["git", "ls-files"], text=True).split()


def main():
    files = git_files()
    go_sources = {f: open(f, errors="ignore").read() for f in files if f.endswith(".go")}
    go_text = "\n".join(go_sources.values())
    code_text = go_text + "\n".join(
        open(f, errors="ignore").read()
        for f in files
        if f.endswith((".sh", ".js", ".yml", ".yaml", ".service")) or f.startswith("deploy/") or f in ("install.sh", "Dockerfile")
    )
    env_vars = set(re.findall(r"STEPANEL_[A-Z0-9_]+", code_text))
    tests = set(re.findall(r"^func ((?:Test|Benchmark|Fuzz)\w+)\(", go_text, re.M))
    funcs = set(re.findall(r"^func (?:\([^)]*\) )?([A-Za-z_]\w*)\(", go_text, re.M))
    types = set(re.findall(r"^type ([A-Za-z_]\w*) ", go_text, re.M))
    # The dashboard catch-all "/" would match every path as a prefix.
    routes = set(re.findall(r'mux\.Handle(?:Func)?\("(?:[A-Z]+ )?(/[^"]*)"', go_text)) - {"/"}
    commands = set(re.findall(r'os\.Args\[1\] == "([a-z-]+)"', go_text))
    known_files = set(files)

    docs = sorted(
        f for f in files
        if f.endswith(".md") and f not in SKIP_FILES and not f.startswith(SKIP_DIRS)
    )
    problems = []

    def report(doc, line_number, kind, ref):
        if (doc, ref) in ALLOWED:
            return
        problems.append(f"{doc}:{line_number}: {kind}: {ref}")

    def file_exists(doc, ref):
        candidates = [ref, os.path.normpath(os.path.join(os.path.dirname(doc), ref))]
        if any(c in known_files or os.path.isdir(c) for c in candidates):
            return True
        if "/" not in ref:
            return any(f.endswith("/" + ref) for f in known_files)
        return False

    for doc in docs:
        in_fence = False
        for number, line in enumerate(open(doc, errors="ignore").read().splitlines(), 1):
            if line.lstrip().startswith("```"):
                in_fence = not in_fence
                continue
            for var in set(re.findall(r"STEPANEL_[A-Z0-9_]+", line)):
                if var not in env_vars and var not in CLIENT_VARIABLES and not var.endswith("_"):
                    report(doc, number, "setting not read by code", var)
            if in_fence:
                continue
            for ref in re.findall(r"(?<![\w/.-])((?:internal|cmd|deploy|scripts|docs|web|tests|\.github)/[\w./-]+\.\w+|[\w-]+\.(?:go|sh|js|css|yaml|yml|md))(?![\w])", line):
                if ref == "Node.js" or "*" in ref:
                    continue
                if not file_exists(doc, ref):
                    report(doc, number, "missing file", ref)
            for name in re.findall(r"\b((?:Test|Benchmark|Fuzz)[A-Z]\w+)", line):
                if name not in tests:
                    report(doc, number, "test not found", name)
            for name in re.findall(r"`(?:[\w.]+\.)?([A-Za-z_]\w*)\(\)`", line):
                if name not in funcs and name not in types and name not in ("MkdirAll",):
                    report(doc, number, "function not found", name + "()")
            for command in re.findall(r"(?:^|[`/ ])stepanel ([a-z][a-z-]+)\b", line):
                if command not in commands and command not in NOT_COMMANDS:
                    report(doc, number, "command not found", "stepanel " + command)
            for route in re.findall(r"(?<![\w.])(/api/[a-z0-9_/{}.-]+)", line):
                base = re.sub(r"\{[^}]+\}.*$", "", route).rstrip("/")
                if not any(
                    r.rstrip("/") == base or (r.endswith("/") and base.startswith(r.rstrip("/")))
                    for r in routes
                ):
                    report(doc, number, "API route not registered", route)

    if problems:
        print("Documentation references that do not exist on this branch:")
        for problem in problems:
            print("  " + problem)
        print(f"\n{len(problems)} problem(s). Fix the document, or add an ALLOWED entry with a reason.")
        return 1
    print(f"✓ Documentation references match the code ({len(docs)} documents checked)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

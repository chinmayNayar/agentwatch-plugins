#!/usr/bin/env python3
"""Mutation check: break each invariant in a scratch copy; its tests must go red.

    python3 scripts/guard_sweep.py
A mutation that stays green means a test that cannot fail. Exit 1 then.
"""
import pathlib, shutil, subprocess, sys, tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]
MUTATIONS = [
    ("dry_run softening removed", "internal/policy/policy.go", "if v.Source == event.SrcSelfProtect || v.Source == event.SrcDeniedTool {", "if true {", "./internal/policy/ ./internal/hook/"),
    ("denied_tools ignored", "internal/policy/policy.go", "if globAny(pol.DeniedTools, names) {", "if false && globAny(pol.DeniedTools, names) {", "./internal/policy/"),
    ("fail_closed ignored", "internal/policy/policy.go", "if cfg.Mode == config.ModeEnforce && cfg.FailMode == config.FailClosed {", "if false {", "./internal/policy/ ./internal/hook/"),
    ("backup not restored", "internal/install/install.go", 'case rec.Existed && rec.BackupPath != "":', "case false:", "./internal/install/"),
    ("openai rule removed", "internal/redact/redact.go", '{"openai_key",', '{"openai_key_x", lazyre.New(`NEVERMATCH`), 0}, // {"openai_key",', "./internal/redact/"),
    ("self-protect off", "internal/selfprotect/selfprotect.go", "if ev.Kind != event.PreTool {", "if true {", "./internal/selfprotect/ ./internal/hook/"),
    ("panic not recovered", "internal/hook/hook.go", "if r := recover(); r != nil {", "if r := recover(); r != nil && false {", "./internal/hook/"),
    ("spool keeps duplicates on retry", "internal/spool/spool.go", "\t\t\tif stop {\n\t\t\t\tbreak\n\t\t\t}", "", "./internal/spool/"),
]

def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        work = pathlib.Path(tmp) / "repo"
        shutil.copytree(REPO, work, ignore=shutil.ignore_patterns(".git", "extensions", "dist"))
        missed = 0
        for name, f, a, b, pkgs in MUTATIONS:
            p = work / f
            orig = p.read_text()
            if a not in orig:
                print("STALE  ", name, "(pattern not found: update this sweep)"); missed += 1; continue
            p.write_text(orig.replace(a, b, 1))
            r = subprocess.run(f"go test -count=1 {pkgs}", shell=True, cwd=work, capture_output=True, text=True)
            p.write_text(orig)
            ok = r.returncode != 0
            missed += not ok
            print("RED    " if ok else "GREEN! ", name)
        return 1 if missed else 0

if __name__ == "__main__":
    sys.exit(main())

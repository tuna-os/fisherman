#!/usr/bin/env python3
"""Decide whether promote.yml may move prod to a dev commit (#272).

The gate judges the push runs of the gated workflows on the candidate
commit, job by job. It does not read the commit's check runs, because the
check runs mix three things that must not gate a release:

- scheduled runs (bootcrew-nightly.yml, build-ssh-images.yml) attach their
  results to whatever commit was at dev HEAD when the cron fired;
- advisory jobs (`advisory / ...`, continue-on-error) report `failure` on
  their check run even though they do not fail their workflow run;
- two workflows can share a job name (`unit-tests` is a job of the VM
  workflow and of the nightly), so a name says nothing about where a check
  came from.

The verdict is one of three:

- promote: every gated workflow has a push run on the candidate, every
  non-advisory job in it finished green or skipped, and every required job
  is present and `success`;
- wait: a non-advisory job is still running. This is the normal answer of
  an early firing; the firing for the last workflow to finish decides. A
  job that has not finished STALL_HOURS after its run started is a refusal;
- refuse: anything else. The workflow fails the Gate job on a refusal, so a
  refused candidate shows red, not as a green run that did nothing.

`decide()` is pure; tests/test-promote-gate.py drives it with recorded
shapes. `main()` fetches the runs and jobs with `gh api`.
"""

import datetime as dt
import json
import os
import subprocess
import sys

# A job whose name starts with this is advisory: it has continue-on-error
# and its result never gates a release. tests/test-promote-gate.py checks
# that the gated workflows keep the name and continue-on-error together.
ADVISORY_PREFIX = "advisory / "

GREEN = {"success", "skipped", "neutral"}


def names(block):
    return [line.strip() for line in (block or "").splitlines() if line.strip()]


def required_jobs(block):
    """`workflow | job` lines -> [(workflow, job)]."""
    out = []
    for line in names(block):
        workflow, sep, job = line.partition("|")
        if not sep or not workflow.strip() or not job.strip():
            raise ValueError(f"REQUIRED_JOBS line is not 'workflow | job': {line!r}")
        out.append((workflow.strip(), job.strip()))
    return out


def parse_time(value):
    if not value:
        return None
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def newest_runs(runs, gated):
    """The newest push run of each gated workflow, by workflow name."""
    picked = {}
    for run in runs:
        if run.get("event") != "push" or run.get("name") not in gated:
            continue
        have = picked.get(run["name"])
        if have is None or run.get("id", 0) > have.get("id", 0):
            picked[run["name"]] = run
    return picked


def decide(runs, jobs_of, gated, required, now, stall_hours=6):
    """Return (verdict, reason). verdict is promote, wait or refuse.

    runs: workflow runs on the candidate (any event; only push runs count).
    jobs_of: callable run -> list of jobs ({name, status, conclusion}) of the
      run's latest attempt.
    gated: workflow names. Each must have a push run on the candidate.
    required: [(workflow, job)] that must be present and `success`.
    """
    picked = newest_runs(runs, gated)
    absent = [w for w in gated if w not in picked]
    if absent:
        return "refuse", ("no push run of " + "; ".join(absent) + " on the candidate "
                          "(the workflow did not start: disabled, a YAML error, "
                          "a path filter or [skip ci])")

    pending, red, missing = [], [], []
    stalled = False
    for workflow in gated:
        run = picked[workflow]
        jobs = jobs_of(run)
        by_name = {}
        for job in jobs:
            by_name.setdefault(job.get("name"), []).append(job)
            if str(job.get("name", "")).startswith(ADVISORY_PREFIX):
                continue
            label = f"{workflow} | {job.get('name')}"
            if job.get("status") != "completed":
                pending.append(label)
            elif job.get("conclusion") not in GREEN:
                red.append(f"{label} ({job.get('conclusion')})")
        run_open = run.get("status") != "completed"
        if run_open and pending:
            started = parse_time(run.get("run_started_at") or run.get("created_at"))
            if started and (now - started).total_seconds() > stall_hours * 3600:
                stalled = True
        for want_workflow, want_job in required:
            if want_workflow != workflow:
                continue
            found = by_name.get(want_job, [])
            if any(j.get("conclusion") == "success" for j in found):
                continue
            if any(j.get("status") != "completed" for j in found):
                continue  # already in pending
            if not found and run_open:
                # A job behind `needs:` may not exist until its turn comes.
                pending.append(f"{workflow} | {want_job} (not started)")
                continue
            if found:
                concl = ",".join(sorted({str(j.get("conclusion")) for j in found}))
                missing.append(f"{workflow} | {want_job} ({concl})")
            else:
                missing.append(f"{workflow} | {want_job} (absent)")

    if red:
        return "refuse", "red jobs: " + "; ".join(red)
    if missing:
        return "refuse", "required jobs not green: " + "; ".join(missing)
    if pending:
        if stalled:
            return "refuse", (f"jobs still not finished after {stall_hours} h: "
                              + "; ".join(pending))
        return "wait", "still running: " + "; ".join(pending)
    return "promote", "every gated job is green"


def gh_lines(path, jq):
    out = subprocess.run(
        ["gh", "api", "--paginate", "-H", "Accept: application/vnd.github+json", path,
         "--jq", jq],
        check=True, capture_output=True, text=True).stdout
    return [json.loads(line) for line in out.splitlines() if line.strip()]


def main():
    repo = os.environ["GITHUB_REPOSITORY"]
    sha = os.environ["SHA"]
    branch = os.environ.get("DEV_BRANCH", "dev")
    gated = names(os.environ["GATED_WORKFLOWS"])
    required = required_jobs(os.environ["REQUIRED_JOBS"])
    stall = float(os.environ.get("STALL_HOURS", "6"))

    runs = gh_lines(
        f"/repos/{repo}/actions/runs?head_sha={sha}&branch={branch}&event=push&per_page=100",
        ".workflow_runs[] | {id, name, event, status, conclusion, run_started_at, "
        "created_at, html_url} | @json")

    def jobs_of(run):
        return gh_lines(
            f"/repos/{repo}/actions/runs/{run['id']}/jobs?filter=latest&per_page=100",
            ".jobs[] | {name, status, conclusion} | @json")

    picked = newest_runs(runs, gated)
    for name in gated:
        run = picked.get(name)
        print(f"{name}: " + (f"{run['status']}/{run.get('conclusion') or '-'} {run.get('html_url', '')}"
                             if run else "no push run"))
    verdict, reason = decide(runs, jobs_of, gated, required,
                             dt.datetime.now(dt.timezone.utc), stall)
    # The last line is the verdict; promote.yml reads it.
    print(f"{verdict}\t{reason}")


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Tests for the promote.yml gate (#272).

Two halves:

- the decision table: scripts/promote-gate.py decide() on run and job
  shapes recorded from this repository (the 137 refusals of #272 among
  them);
- the names: GATED_WORKFLOWS, REQUIRED_JOBS and on.workflow_run in
  promote.yml checked against the workflow files, so that a rename, a path
  filter or a schedule-only trigger cannot make the gate block forever or
  judge a check that must not gate.

No network; run with `python3 tests/test-promote-gate.py`.
"""

import datetime as dt
import fnmatch
import importlib.util
import pathlib
import re
import unittest

import yaml

REPO = pathlib.Path(__file__).resolve().parents[1]
WORKFLOWS = REPO / ".github" / "workflows"
PROMOTE = WORKFLOWS / "promote.yml"

spec = importlib.util.spec_from_file_location("promote_gate", REPO / "scripts" / "promote-gate.py")
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)

VM = "Bootcrew VM Boot (E2E tests)"
LINT = "Workflow lint"
CM = "customMounts E2E (manual layout)"
NIGHTLY = "Bootcrew Nightly (full install)"
SSH = "Build SSH-enabled Images"
GATED = [VM, LINT, CM]
REQUIRED = [(VM, "required-canaries"), (VM, "lint"), (VM, "unit-tests"),
            (LINT, "actionlint"), (CM, "customMounts E2E disk layout")]
NOW = dt.datetime(2026, 11, 12, 12, 0, tzinfo=dt.timezone.utc)


def job(name, conclusion="success", status="completed"):
    return {"name": name, "status": status,
            "conclusion": conclusion if status == "completed" else None}


def vm_jobs(**override):
    names = ["lint", "setup", "unit-tests", "tui-unit-tests", "required / dakota",
             "required / centos-bootc", "required-canaries", "advisory / arm64 unit tests",
             "advisory / arch-bootc-luks", "advisory / store-reuse / plain"]
    return [override.get(n, job(n)) for n in names]


def world(vm=None, lint=None, cm=None, extra_runs=(), status=None):
    """Runs + jobs on one candidate. A value of False drops that run."""
    jobs = {}
    runs = []
    for rid, (name, js) in enumerate([(VM, vm), (LINT, lint), (CM, cm)], start=10):
        if js is False:
            continue
        if js is None:
            js = {VM: vm_jobs(), LINT: [job("actionlint")],
                  CM: [job("customMounts E2E disk layout")]}[name]
        done = all(j["status"] == "completed" for j in js)
        runs.append({"id": rid, "name": name, "event": "push",
                     "status": (status or {}).get(name, "completed" if done else "in_progress"),
                     "conclusion": None, "run_started_at": "2026-11-12T11:00:00Z"})
        jobs[rid] = js
    for run in extra_runs:
        runs.append(run)
        jobs[run["id"]] = run.pop("_jobs", [])
    return runs, (lambda run: jobs[run["id"]])


def decide(runs_jobs, **kw):
    runs, jobs_of = runs_jobs
    return gate.decide(runs, jobs_of, GATED, REQUIRED, NOW, **kw)


class Decision(unittest.TestCase):
    def assertVerdict(self, got, verdict, *fragments):
        self.assertEqual(got[0], verdict, got[1])
        for f in fragments:
            self.assertIn(f, got[1])

    def test_all_green_promotes(self):
        self.assertVerdict(decide(world()), "promote")

    def test_474272b_advisory_red_promotes(self):
        # #272: required green, advisory legs red. Advisory never gates.
        vm = vm_jobs(**{"advisory / arch-bootc-luks": job("advisory / arch-bootc-luks", "failure"),
                        "advisory / arm64 unit tests": job("advisory / arm64 unit tests", "failure")})
        self.assertVerdict(decide(world(vm=vm)), "promote")

    def test_5a98ec8_scheduled_red_promotes(self):
        # #272: nightly and SSH image rebuild legs attached to the candidate.
        nightly = {"id": 90, "name": NIGHTLY, "event": "schedule", "status": "completed",
                   "_jobs": [job("unit-tests", "failure"), job("install / opensuse-bootc", "failure")]}
        ssh = {"id": 91, "name": SSH, "event": "schedule", "status": "completed",
               "_jobs": [job("build / debian-bootc", "failure")]}
        self.assertVerdict(decide(world(extra_runs=[nightly, ssh])), "promote")

    def test_dispatch_and_pr_runs_do_not_count(self):
        # A workflow_dispatch run of a gated workflow does not stand in for
        # the push run, and a red one does not block.
        other = {"id": 92, "name": CM, "event": "workflow_dispatch", "status": "completed",
                 "_jobs": [job("customMounts E2E disk layout", "failure")]}
        self.assertVerdict(decide(world(extra_runs=[other])), "promote")
        self.assertVerdict(decide(world(cm=False, extra_runs=[
            {"id": 93, "name": CM, "event": "workflow_dispatch", "status": "completed",
             "_jobs": [job("customMounts E2E disk layout")]}])), "refuse", CM)

    def test_advisory_still_running_does_not_wait(self):
        # #272 root cause 3: ~75 min advisory legs held the gate.
        vm = vm_jobs(**{"advisory / arch-bootc-luks":
                        job("advisory / arch-bootc-luks", status="in_progress")})
        self.assertVerdict(decide(world(vm=vm, status={VM: "in_progress"})), "promote")

    def test_absent_gated_run_refuses(self):
        # The old actionlint path filter: no run, no check, refused forever
        # while green. Now that is a refusal that fails the job.
        self.assertVerdict(decide(world(lint=False)), "refuse", LINT, "did not start")

    def test_red_required_refuses(self):
        vm = vm_jobs(**{"unit-tests": job("unit-tests", "failure"),
                        "required-canaries": job("required-canaries", "failure")})
        self.assertVerdict(decide(world(vm=vm)), "refuse", "unit-tests (failure)",
                           "required-canaries (failure)")

    def test_red_actionlint_refuses(self):
        self.assertVerdict(decide(world(lint=[job("actionlint", "failure")])),
                           "refuse", "actionlint (failure)")

    def test_red_unrequired_job_refuses(self):
        # tui-unit-tests is not named but is not advisory: it gates.
        vm = vm_jobs(**{"tui-unit-tests": job("tui-unit-tests", "timed_out")})
        self.assertVerdict(decide(world(vm=vm)), "refuse", "tui-unit-tests (timed_out)")

    def test_cancelled_job_refuses(self):
        self.assertVerdict(decide(world(cm=[job("customMounts E2E disk layout", "cancelled")])),
                           "refuse", "cancelled")

    def test_skipped_required_refuses(self):
        vm = vm_jobs(**{"required-canaries": job("required-canaries", "skipped")})
        self.assertVerdict(decide(world(vm=vm)), "refuse", "required-canaries (skipped)")

    def test_renamed_required_job_refuses(self):
        self.assertVerdict(decide(world(cm=[job("customMounts E2E")])),
                           "refuse", "customMounts E2E disk layout (absent)")

    def test_running_waits(self):
        vm = vm_jobs(**{"lint": job("lint", status="in_progress")})
        self.assertVerdict(decide(world(vm=vm)), "wait", "lint")

    def test_required_not_created_yet_waits(self):
        # A job behind `needs:` has no entry until its turn.
        vm = [j for j in vm_jobs() if j["name"] != "required-canaries"]
        got = decide(world(vm=vm, status={VM: "in_progress"}))
        self.assertVerdict(got, "wait", "required-canaries (not started)")

    def test_red_beats_wait(self):
        vm = vm_jobs(**{"lint": job("lint", "failure"),
                        "required / dakota": job("required / dakota", status="in_progress")})
        self.assertVerdict(decide(world(vm=vm)), "refuse", "lint (failure)")

    def test_stalled_refuses(self):
        vm = vm_jobs(**{"required / dakota": job("required / dakota", status="queued")})
        runs, jobs_of = world(vm=vm)
        for r in runs:
            r["run_started_at"] = "2026-11-12T01:00:00Z"
        self.assertVerdict(gate.decide(runs, jobs_of, GATED, REQUIRED, NOW, stall_hours=6),
                           "refuse", "after 6")

    def test_rerun_newest_run_wins(self):
        runs, jobs_of = world(cm=[job("customMounts E2E disk layout", "failure")])
        runs.append({"id": 99, "name": CM, "event": "push", "status": "completed"})
        jobs = {99: [job("customMounts E2E disk layout")]}
        self.assertVerdict(
            gate.decide(runs, lambda r: jobs.get(r["id"]) or jobs_of(r), GATED, REQUIRED, NOW),
            "promote")

    def test_required_jobs_parser(self):
        self.assertEqual(gate.required_jobs(" A | b c \n\n D|e\n"), [("A", "b c"), ("D", "e")])
        with self.assertRaises(ValueError):
            gate.required_jobs("no separator")


class Main(unittest.TestCase):
    """main() with a fake `gh` on PATH: the plumbing, not the API."""

    def run_main(self, cm_conclusion):
        import json, os, subprocess, sys, tempfile
        with tempfile.TemporaryDirectory() as tmp:
            runs = [{"id": 10, "name": VM, "event": "push", "status": "completed"},
                    {"id": 11, "name": LINT, "event": "push", "status": "completed"},
                    {"id": 12, "name": CM, "event": "push", "status": "completed"}]
            jobs = {"10": [job("lint"), job("unit-tests"), job("required-canaries")],
                    "11": [job("actionlint")],
                    "12": [job("customMounts E2E disk layout", cm_conclusion)]}
            data = pathlib.Path(tmp, "data.json")
            data.write_text(json.dumps({"runs": runs, "jobs": jobs}))
            fake = pathlib.Path(tmp, "gh")
            fake.write_text(
                "#!/usr/bin/env python3\n"
                "import json, re, sys\n"
                f"d = json.load(open({str(data)!r}))\n"
                "path = next(a for a in sys.argv if a.startswith('/repos/'))\n"
                "m = re.search(r'/runs/(\\d+)/jobs', path)\n"
                "assert 'event=push' in path or m, path\n"
                "for item in (d['jobs'][m.group(1)] if m else d['runs']):\n"
                "    print(json.dumps(item))\n")
            fake.chmod(0o755)
            env = dict(os.environ, PATH=f"{tmp}:{os.environ['PATH']}",
                       GITHUB_REPOSITORY="o/r", SHA="abc", DEV_BRANCH="dev",
                       GATED_WORKFLOWS="\n".join(GATED),
                       REQUIRED_JOBS="\n".join(f"{w} | {j}" for w, j in REQUIRED))
            out = subprocess.run([sys.executable, str(REPO / "scripts" / "promote-gate.py")],
                                 env=env, capture_output=True, text=True, check=True).stdout
            return out.strip().splitlines()[-1].split("\t", 1)

    def test_main_promotes(self):
        self.assertEqual(self.run_main("success")[0], "promote")

    def test_main_refuses(self):
        verdict, reason = self.run_main("failure")
        self.assertEqual(verdict, "refuse")
        self.assertIn("customMounts E2E disk layout (failure)", reason)


# ---- the names in promote.yml against the workflow files ----------------

def load(path):
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    doc["on"] = doc.pop(True, doc.get("on"))  # YAML 1.1 reads `on` as true
    if isinstance(doc["on"], str):
        doc["on"] = {doc["on"]: None}
    elif isinstance(doc["on"], list):
        doc["on"] = {e: None for e in doc["on"]}
    return doc


def all_workflows():
    files = sorted(WORKFLOWS.glob("*.yml")) + sorted(WORKFLOWS.glob("*.yaml"))
    return {load(p)["name"]: load(p) for p in files}


def push_to_dev(doc):
    """The push trigger config when a push to dev starts it, else None."""
    if "push" not in doc["on"]:
        return None
    cfg = doc["on"]["push"] or {}
    if "branches" in cfg and not any(fnmatch.fnmatchcase("dev", b) for b in cfg["branches"]):
        return None
    if "branches-ignore" in cfg and any(fnmatch.fnmatchcase("dev", b) for b in cfg["branches-ignore"]):
        return None
    if "branches" not in cfg and ("tags" in cfg or "tags-ignore" in cfg):
        return None
    return cfg


def job_name(job_id, job):
    return str(job.get("name", job_id))


class Names(unittest.TestCase):
    def setUp(self):
        self.promote = load(PROMOTE)
        self.env = self.promote["env"]
        self.gated = gate.names(self.env["GATED_WORKFLOWS"])
        self.required = gate.required_jobs(self.env["REQUIRED_JOBS"])
        self.wf = all_workflows()

    def test_fixture_matches_promote_yml(self):
        self.assertEqual(self.gated, GATED)
        self.assertEqual(self.required, REQUIRED)

    def test_workflow_names_are_unique(self):
        files = sorted(WORKFLOWS.glob("*.yml")) + sorted(WORKFLOWS.glob("*.yaml"))
        seen = [load(p).get("name") for p in files]
        self.assertNotIn(None, seen)
        self.assertEqual(len(seen), len(set(seen)))

    def test_triggers_equal_gated(self):
        # The gate decides on the last gated workflow's completion, so the
        # firing list and the judged list must be the same set.
        self.assertEqual(sorted(self.promote["on"]["workflow_run"]["workflows"]),
                         sorted(self.gated))
        self.assertEqual(self.promote["on"]["workflow_run"]["branches"], ["dev"])

    def test_gated_workflows_run_on_every_push_to_dev(self):
        for name in self.gated:
            self.assertIn(name, self.wf, f"GATED_WORKFLOWS names no workflow: {name!r}")
            cfg = push_to_dev(self.wf[name])
            self.assertIsNotNone(cfg, f"{name!r} does not run on a push to dev")
            self.assertFalse({"paths", "paths-ignore"} & set(cfg),
                             f"{name!r} has a push path filter: it leaves no run on most "
                             "commits and the gate would refuse them (#272)")

    def test_scheduled_only_workflows_are_not_gated(self):
        for name, doc in self.wf.items():
            if "schedule" in doc["on"] and push_to_dev(doc) is None:
                self.assertNotIn(name, self.gated)
        self.assertNotIn(NIGHTLY, self.gated)
        self.assertNotIn(SSH, self.gated)

    def test_unfiltered_dev_workflows_are_gated(self):
        # A workflow that runs on every push to dev and is not gated would
        # be a check the release ignores. Name it here with the reason.
        not_gated = {
            # Checks the action lockfile and lints workflows. It blocks pull
            # requests; a release does not wait on it.
            "actions-security": "pull-request check on workflow files",
        }
        for name, doc in self.wf.items():
            cfg = push_to_dev(doc)
            if cfg is None or {"paths", "paths-ignore"} & set(cfg) or name == self.promote["name"]:
                continue
            if name not in self.gated:
                self.assertIn(name, not_gated, f"{name!r} runs on every push to dev but "
                              "promote.yml does not gate on it")

    def test_required_jobs_exist_and_cannot_be_skipped_silently(self):
        for workflow, want in self.required:
            jobs = self.wf[workflow]["jobs"]
            hits = [(jid, j) for jid, j in jobs.items() if job_name(jid, j) == want]
            self.assertEqual(len(hits), 1, f"{workflow!r} has no single job named {want!r}")
            jid, j = hits[0]
            self.assertNotIn("${{", want)
            self.assertFalse(j.get("continue-on-error"), f"{want!r} is continue-on-error")
            cond = str(j.get("if", ""))
            self.assertTrue(not cond or "always()" in cond,
                            f"{want!r} has `if: {cond}`; a skipped required job refuses")

    def test_advisory_name_means_continue_on_error(self):
        # The gate ignores jobs named `advisory / ...`. That is safe only if
        # exactly the continue-on-error jobs carry the name.
        for name in self.gated:
            for jid, j in self.wf[name]["jobs"].items():
                advisory = job_name(jid, j).startswith(gate.ADVISORY_PREFIX)
                coe = j.get("continue-on-error")
                self.assertEqual(advisory, coe is True,
                                 f"{name} job {jid!r}: name {job_name(jid, j)!r} vs "
                                 f"continue-on-error {coe!r}")

    def test_old_check_run_sweep_is_gone(self):
        text = PROMOTE.read_text(encoding="utf-8")
        self.assertNotIn("/check-runs", text)
        self.assertNotIn("REQUIRED_CHECKS", text)
        self.assertIn("scripts/promote-gate.py", text)
        self.assertIn("actions: read", text)

    def test_refusal_fails_the_job(self):
        text = PROMOTE.read_text(encoding="utf-8")
        block = re.search(r"case \"\$verdict\" in(.*?)esac", text, re.S).group(1)
        refuse = block.split("*)", 1)[1]
        self.assertIn("exit 1", refuse)
        self.assertNotIn("exit 1", block.split("*)", 1)[0])


if __name__ == "__main__":
    unittest.main()

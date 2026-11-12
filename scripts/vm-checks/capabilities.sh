# shellcheck shell=bash
# Guest check: the capability report (`fisherman probe --json --source`,
# docs/PROBE.md "Capabilities") answered for the bootc that the install
# then used (#383). ADVISORY: only the store-reuse profile runs it
# (scripts/store-reuse-vm.sh), and that job is continue-on-error.
#
# The profile takes two reports inside the VM that installs, before the
# install: CAPS_PROBE for the booted image pinned by its digest, and
# CAPS_PROBE_OTHER for the same name pinned by a digest the host did not
# boot. The install then runs bootc from a verified copy of the booted image
# (reuseInstallOptions), so the installed system is that image and its bootc
# is the installing bootc. This check runs on the installed system's first
# boot (boot-verify.sh, BOOTCREW_GUEST_CHECKS), with ssh_root set, and holds
# the report to what the installed system shows.
#
# Env: CAPS_PROBE, CAPS_PROBE_OTHER (files on the runner); CAPS_LIVE_BOOTC
# (the first line of `bootc --version` in the installing VM);
# CAPS_SOURCE_DIGEST (the digest the installing VM booted);
# CAPS_EXPECT_BACKEND ("ostree" or "composefs", from the recipe).
set -u
: "${CAPS_PROBE:?}" "${CAPS_PROBE_OTHER:?}" "${CAPS_LIVE_BOOTC:?}" \
  "${CAPS_SOURCE_DIGEST:?}" "${CAPS_EXPECT_BACKEND:?}"

facts=$(ssh_root 'echo "bootc: $(bootc --version 2>/dev/null | head -n 1)"
  echo "kernel: $(uname -r)"
  if [ -d /boot/grub2 ] || [ -d /boot/grub ]; then echo "bootloader: grub"
  elif [ -e /boot/EFI/systemd ] || [ -e /boot/efi/EFI/systemd ] || [ -e /efi/EFI/systemd ]; then echo "bootloader: systemd"
  else echo "bootloader: unknown"; fi
  echo "status: $(bootc status --json 2>/dev/null | tr -d "\n")"' 2>/dev/null || true)
echo "$facts" | grep -v '^status: ' | sed 's/^/  /'

python3 -I - "$CAPS_PROBE" "$CAPS_PROBE_OTHER" "$CAPS_LIVE_BOOTC" "$CAPS_SOURCE_DIGEST" \
  "$CAPS_EXPECT_BACKEND" "$facts" <<'PY'
import json, re, sys

probe_path, other_path, live_line, source_digest, want_backend, facts_text = sys.argv[1:7]
fail = 0

def ok(msg):
    print("✅ " + msg)

def bad(msg):
    global fail
    fail = 1
    print("❌ " + msg)

def load(path):
    try:
        return json.load(open(path))["capabilities"]
    except Exception:
        return None

# The probe's own rule for `bootc --version` (internal/probe/bootc.go).
VERSION_RE = re.compile(r"^bootc\s+v?([0-9]+(?:\.[0-9]+){1,3}(?:[-+~][0-9A-Za-z.+~-]{0,40})?)$")

def version(line):
    m = VERSION_RE.match(line.strip())
    return m.group(1) if m else ""

facts = {}
for line in facts_text.splitlines():
    key, sep, value = line.partition(": ")
    if sep:
        facts[key.strip()] = value.strip()
installed_backend = ""
try:
    booted = json.loads(facts.get("status", ""))["status"]["booted"]
    if booted.get("composefs"):
        installed_backend = "composefs"
    elif booted.get("ostree"):
        installed_backend = "ostree"
except Exception:
    pass

SOURCE_FEATURES = ["native_inspection", "install_configuration", "finalization",
                   "deployment_mount", "bootc_image_storage"]

p = load(probe_path)
o = load(other_path)
if p is None or o is None:
    bad("a capability report is missing or is not probe JSON with a capabilities object")
    sys.exit(1)
if p.get("version") != 1 or o.get("version") != 1:
    bad("capabilities.version is not 1 (%r, %r)" % (p.get("version"), o.get("version")))

# 1. The source is the booted image, and its root is the installing host's.
src = p.get("source") or {}
if src.get("digest") == source_digest and src.get("digest_from") == "reference":
    ok("source: the booted digest %s, from the reference" % source_digest)
else:
    bad("source: digest %r from %r, want %s from the reference"
        % (src.get("digest"), src.get("digest_from"), source_digest))
root = p.get("execution_root") or {}
if root.get("established") is True and root.get("reason") == "digest_match" \
        and root.get("context") == "live-host":
    ok("execution root: established on the installing host (digest_match)")
else:
    bad("execution root: established=%r reason=%r context=%r, want true/digest_match/live-host"
        % (root.get("established"), root.get("reason"), root.get("context")))

# 2. The bootc that answered is the bootc that installed: the installing
#    host's, and the installed system's (the same image).
live_v = version(live_line)
installed_v = version(facts.get("bootc", ""))
report_v = root.get("bootc_version") or ""
if report_v and report_v == live_v == installed_v:
    ok("bootc version: the report, the installing host and the installed system all say %s" % report_v)
else:
    bad("bootc version: report %r, installing host %r, installed system %r"
        % (report_v, live_v, installed_v))

# 3. The backend: the report's, the recipe's, and the installed system's.
if root.get("backend") == want_backend == installed_backend \
        and (p.get("live_host") or {}).get("backend") == want_backend:
    ok("backend: %s in the report, the recipe and the installed system" % want_backend)
else:
    bad("backend: report %r (live host %r), recipe %r, installed system %r"
        % (root.get("backend"), (p.get("live_host") or {}).get("backend"),
           want_backend, installed_backend or None))

# 4. Every source feature was answered in the source root, never from
#    nothing; supported only by observation.
feats = p.get("features") or {}
wrong = []
for name in SOURCE_FEATURES:
    f = feats.get(name) or {}
    if f.get("context") != "source-root" or f.get("reason") == "source_root_unavailable" \
            or (f.get("status") == "supported" and f.get("reason") != "observed"):
        wrong.append("%s=%s/%s/%s" % (name, f.get("status"), f.get("reason"), f.get("context")))
us = feats.get("unified_storage") or {}
if (us.get("status"), us.get("reason")) != ("unsupported", "fisherman_refuses"):
    wrong.append("unified_storage=%s/%s" % (us.get("status"), us.get("reason")))
if wrong:
    bad("features: " + ", ".join(wrong))
else:
    ok("features: " + ", ".join("%s=%s" % (n, feats[n]["status"]) for n in SOURCE_FEATURES))

# 5. What the source root said about its defaults and its image is what the
#    install did.
defaults = p.get("install_defaults") or {}
want_loader = defaults.get("bootloader") or ""
got_loader = facts.get("bootloader", "unknown")
if defaults.get("observed") and want_loader:
    if want_loader.startswith(got_loader) and got_loader != "unknown":
        ok("bootloader: print-configuration says %s; the installed system has %s" % (want_loader, got_loader))
    else:
        bad("bootloader: print-configuration says %s; the installed system has %s" % (want_loader, got_loader))
else:
    print("ℹ️  bootloader: print-configuration names none (observed=%s); installed: %s"
          % (defaults.get("observed"), got_loader))
image = p.get("image") or {}
if image.get("observed") and image.get("kernel_version"):
    if image["kernel_version"] == facts.get("kernel", ""):
        ok("kernel: container inspect says %s; the installed system runs it" % image["kernel_version"])
    else:
        bad("kernel: container inspect says %s; the installed system runs %r"
            % (image["kernel_version"], facts.get("kernel")))
else:
    print("ℹ️  kernel: container inspect reported none (observed=%s)" % image.get("observed"))

# 6. Negative: the same host and name, another digest. The host's bootc
#    must not answer for an image it did not boot.
oroot = o.get("execution_root") or {}
ofeats = o.get("features") or {}
leaked = [n for n in SOURCE_FEATURES
          if ((ofeats.get(n) or {}).get("status"), (ofeats.get(n) or {}).get("reason"),
              (ofeats.get(n) or {}).get("context")) != ("unknown", "source_root_unavailable", "none")]
if oroot.get("established") is False and oroot.get("reason") == "digest_mismatch" \
        and not oroot.get("bootc_version") and not leaked \
        and not (o.get("install_defaults") or {}).get("observed") \
        and not (o.get("image") or {}).get("observed"):
    ok("another digest: digest_mismatch, nothing answered by the host's bootc")
else:
    bad("another digest: established=%r reason=%r bootc_version=%r, answered %s, defaults observed=%r, image observed=%r"
        % (oroot.get("established"), oroot.get("reason"), oroot.get("bootc_version"), leaked,
           (o.get("install_defaults") or {}).get("observed"), (o.get("image") or {}).get("observed")))
sys.exit(fail)
PY

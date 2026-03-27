# views/disk.py — disk, filesystem, encryption, and hostname selection
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

import os
import subprocess
from gettext import gettext as _

from gi.repository import Adw, GLib, Gtk


def _list_disks():
    """Return list of (path, label) tuples for installable block devices."""
    disks = []
    try:
        out = subprocess.check_output(
            ["lsblk", "-J", "-o", "NAME,SIZE,MODEL,TYPE,RM,RO"],
            text=True
        )
        import json
        data = json.loads(out)
        for dev in data.get("blockdevices", []):
            if dev.get("type") != "disk":
                continue
            if dev.get("ro"):
                continue
            path = f"/dev/{dev['name']}"
            model = dev.get("model") or dev["name"]
            size = dev.get("size", "")
            label = f"{model} ({size}) — {path}"
            if os.environ.get("TUNAOS_INSTALLER_DEV"):
                # Also show loop devices in dev mode
                pass
            disks.append((path, label))

        if os.environ.get("TUNAOS_INSTALLER_DEV"):
            try:
                loop_out = subprocess.check_output(
                    ["losetup", "--list", "-J"], text=True
                )
                loop_data = json.loads(loop_out)
                for ldev in loop_data.get("loopdevices", []):
                    path = ldev["name"]
                    back = ldev.get("back-file", "unknown")
                    label = f"[DEV: loopback] {os.path.basename(back)} — {path}"
                    disks.append((path, label))
            except Exception:
                pass

    except Exception as e:
        pass
    return disks


@Gtk.Template(resource_path="/org/tunaos/Installer/gtk/disk.ui")
class TunaDisk(Adw.Bin):
    __gtype_name__ = "TunaDisk"

    disk_row = Gtk.Template.Child()
    filesystem_row = Gtk.Template.Child()
    subvolumes_row = Gtk.Template.Child()
    encryption_row = Gtk.Template.Child()
    passphrase_row = Gtk.Template.Child()
    hostname_row = Gtk.Template.Child()
    btn_next = Gtk.Template.Child()
    btn_back = Gtk.Template.Child()

    def __init__(self, window, **kwargs):
        super().__init__(**kwargs)
        self.__window = window
        self.__disks = []

        self.__populate_disks()
        self.__setup_signals()

    def __populate_disks(self):
        self.__disks = _list_disks()
        model = Gtk.StringList()
        for _, label in self.__disks:
            model.append(label)
        self.disk_row.set_model(model)

    def __setup_signals(self):
        self.filesystem_row.connect("notify::selected", self.__on_filesystem_changed)
        self.encryption_row.connect("notify::selected", self.__on_encryption_changed)
        self.btn_next.connect("clicked", self.__on_next)
        self.btn_back.connect("clicked", self.__on_back)

    def __on_filesystem_changed(self, row, *args):
        # 0=xfs, 1=btrfs (subvolumes)
        is_btrfs = row.get_selected() == 1
        self.subvolumes_row.set_visible(is_btrfs)

    def __on_encryption_changed(self, row, *args):
        # 0=none, 1=TPM2-LUKS, 2=Passphrase
        is_passphrase = row.get_selected() == 2
        self.passphrase_row.set_visible(is_passphrase)

    def __on_next(self, *args):
        recipe = self.__window.recipe

        # Disk
        idx = self.disk_row.get_selected()
        if idx < len(self.__disks):
            recipe["disk"] = self.__disks[idx][0]

        # Filesystem
        fs_idx = self.filesystem_row.get_selected()
        if fs_idx == 1:
            recipe["filesystem"] = "btrfs"
            recipe["btrfsSubvolumes"] = True
        else:
            recipe["filesystem"] = "xfs"
            recipe["btrfsSubvolumes"] = False

        # Encryption
        enc_idx = self.encryption_row.get_selected()
        if enc_idx == 0:
            recipe["encryption"] = {"type": "none", "passphrase": ""}
        elif enc_idx == 1:
            recipe["encryption"] = {"type": "tpm2-luks", "passphrase": ""}
        else:
            passphrase = self.passphrase_row.get_text()
            recipe["encryption"] = {"type": "luks-passphrase", "passphrase": passphrase}

        # Hostname
        hostname = self.hostname_row.get_text().strip()
        if hostname:
            recipe["hostname"] = hostname

        self.__window.navigate_to("confirm")

    def __on_back(self, *args):
        self.__window.navigate_to("welcome")

    def prepare(self):
        # Refresh disk list each time we enter
        self.__populate_disks()

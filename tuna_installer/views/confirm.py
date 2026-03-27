# views/confirm.py — summary before installation
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

from gettext import gettext as _
from gi.repository import Adw, Gtk


@Gtk.Template(resource_path="/org/tunaos/Installer/gtk/confirm.ui")
class TunaConfirm(Adw.Bin):
    __gtype_name__ = "TunaConfirm"

    disk_label = Gtk.Template.Child()
    filesystem_label = Gtk.Template.Child()
    encryption_label = Gtk.Template.Child()
    hostname_label = Gtk.Template.Child()
    image_label = Gtk.Template.Child()
    btn_install = Gtk.Template.Child()
    btn_back = Gtk.Template.Child()

    def __init__(self, window, **kwargs):
        super().__init__(**kwargs)
        self.__window = window
        self.btn_install.connect("clicked", self.__on_install)
        self.btn_back.connect("clicked", self.__on_back)

    def prepare(self):
        r = self.__window.recipe

        self.disk_label.set_label(r.get("disk", "—"))

        fs = r.get("filesystem", "xfs")
        if r.get("btrfsSubvolumes"):
            fs += _(" (with subvolumes)")
        self.filesystem_label.set_label(fs)

        enc = r.get("encryption", {}).get("type", "none")
        enc_labels = {
            "none": _("None"),
            "tpm2-luks": _("TPM2 (automatic unlock)"),
            "luks-passphrase": _("LUKS passphrase"),
        }
        self.encryption_label.set_label(enc_labels.get(enc, enc))

        self.hostname_label.set_label(r.get("hostname", "tunaos"))
        self.image_label.set_label(r.get("image", "—"))

    def __on_install(self, *args):
        self.__window.start_installation()

    def __on_back(self, *args):
        self.__window.navigate_to("disk")

# views/done.py — installation complete page
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

from gettext import gettext as _
from gi.repository import Adw, Gtk
import subprocess


@Gtk.Template(resource_path="/org/tunaos/Installer/gtk/done.ui")
class TunaDone(Adw.Bin):
    __gtype_name__ = "TunaDone"

    status_icon = Gtk.Template.Child()
    title_label = Gtk.Template.Child()
    subtitle_label = Gtk.Template.Child()
    btn_reboot = Gtk.Template.Child()

    def __init__(self, window, **kwargs):
        super().__init__(**kwargs)
        self.__window = window
        self.btn_reboot.connect("clicked", self.__on_reboot)

    def set_result(self, success: bool):
        if success:
            self.status_icon.set_from_icon_name("emblem-ok-symbolic")
            self.title_label.set_label(_("Installation Complete"))
            self.subtitle_label.set_label(
                _("TunaOS has been installed. Reboot to start using your new system.")
            )
            self.btn_reboot.set_visible(True)
        else:
            self.status_icon.set_from_icon_name("dialog-error-symbolic")
            self.title_label.set_label(_("Installation Failed"))
            self.subtitle_label.set_label(
                _("Something went wrong. Check the console output for details.")
            )
            self.btn_reboot.set_visible(False)

    def __on_reboot(self, *args):
        subprocess.Popen(["systemctl", "reboot"])

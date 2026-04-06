# main.py
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
gi.require_version("Vte", "3.91")

import logging
import sys

from gi.repository import Adw, Gio

from bootc_installer.window import TunaInstallerWindow

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("TunaInstaller::Main")


class TunaInstallerApp(Adw.Application):
    def __init__(self):
        super().__init__(
            application_id="org.bootcinstaller.Installer",
            flags=Gio.ApplicationFlags.FLAGS_NONE,
        )
        self.create_action("quit", self.close, ["<primary>q"])

    def do_activate(self):
        win = self.props.active_window
        if not win:
            win = TunaInstallerWindow(application=self)
        win.present()

    def create_action(self, name, callback, shortcuts=None):
        action = Gio.SimpleAction.new(name, None)
        action.connect("activate", callback)
        self.add_action(action)
        if shortcuts:
            self.set_accels_for_action(f"app.{name}", shortcuts)

    def close(self, *args):
        self.quit()


def main(version):
    app = TunaInstallerApp()
    return app.run(sys.argv)

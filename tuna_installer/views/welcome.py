# views/welcome.py
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

from gettext import gettext as _
from gi.repository import Adw, Gtk


@Gtk.Template(resource_path="/org/bootcinstaller/Installer/gtk/welcome.ui")
class TunaWelcome(Adw.Bin):
    __gtype_name__ = "TunaWelcome"

    btn_install = Gtk.Template.Child()
    btn_try = Gtk.Template.Child()

    def __init__(self, window, **kwargs):
        super().__init__(**kwargs)
        self.__window = window
        self.btn_install.connect("clicked", self.__on_install)
        self.btn_try.connect("clicked", self.__on_try)

    def __on_install(self, *args):
        self.__window.navigate_to("disk")

    def __on_try(self, *args):
        import sys
        sys.exit(0)

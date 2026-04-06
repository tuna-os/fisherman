# window.py — main application window, manages page navigation
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

import json
import logging

from gi.repository import Adw, Gtk

from bootc_installer.views.welcome import TunaWelcome
from bootc_installer.views.disk import TunaDisk
from bootc_installer.views.confirm import TunaConfirm
from bootc_installer.views.progress import TunaProgress
from bootc_installer.views.done import TunaDone

logger = logging.getLogger("TunaInstaller::Window")

FISHERMAN_BIN = "/usr/local/bin/fisherman"


@Gtk.Template(resource_path="/org/bootcinstaller/Installer/gtk/window.ui")
class TunaInstallerWindow(Adw.ApplicationWindow):
    __gtype_name__ = "TunaInstallerWindow"

    view_stack = Gtk.Template.Child()

    def __init__(self, **kwargs):
        super().__init__(**kwargs)

        # Shared install state — populated as user progresses through pages
        self.recipe = {
            "disk": "",
            "filesystem": "xfs",
            "btrfsSubvolumes": False,
            "encryption": {"type": "none", "passphrase": ""},
            "image": "",
            "targetImgref": "",
            "selinuxDisabled": True,
            "hostname": "tunaos",
        }

        self._welcome = TunaWelcome(self)
        self._disk = TunaDisk(self)
        self._confirm = TunaConfirm(self)
        self._progress = TunaProgress(self)
        self._done = TunaDone(self)

        self.view_stack.add_named(self._welcome, "welcome")
        self.view_stack.add_named(self._disk, "disk")
        self.view_stack.add_named(self._confirm, "confirm")
        self.view_stack.add_named(self._progress, "progress")
        self.view_stack.add_named(self._done, "done")

        self.view_stack.set_visible_child_name("welcome")

    def navigate_to(self, page_name: str):
        child = self.view_stack.get_child_by_name(page_name)
        if child and hasattr(child, "prepare"):
            child.prepare()
        self.view_stack.set_visible_child_name(page_name)

    def start_installation(self):
        """Write recipe to /tmp and kick off fisherman via the progress view."""
        import tempfile, os

        recipe_path = "/tmp/fisherman-recipe.json"
        with open(recipe_path, "w") as f:
            json.dump(self.recipe, f, indent=2)

        logger.info("Recipe written to %s: %s", recipe_path, self.recipe)
        self.navigate_to("progress")
        self._progress.start(recipe_path)

    def installation_finished(self, success: bool):
        self.navigate_to("done")
        self._done.set_result(success)

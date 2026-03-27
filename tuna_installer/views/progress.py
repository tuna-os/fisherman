# views/progress.py — installation progress with VTE terminal
#
# Copyright 2026 TunaOS contributors
# SPDX-License-Identifier: GPL-3.0-only

from gettext import gettext as _
from gi.repository import Adw, Gdk, GLib, Gtk, Pango, Vte


FISHERMAN_BIN = "/usr/local/bin/fisherman"


@Gtk.Template(resource_path="/org/tunaos/Installer/gtk/progress.ui")
class TunaProgress(Adw.Bin):
    __gtype_name__ = "TunaProgress"

    progressbar = Gtk.Template.Child()
    status_label = Gtk.Template.Child()
    console_box = Gtk.Template.Child()
    console_button = Gtk.Template.Child()

    def __init__(self, window, **kwargs):
        super().__init__(**kwargs)
        self.__window = window
        self.__terminal = Vte.Terminal()

        font = Pango.FontDescription()
        font.set_family("Monospace")
        font.set_size(12 * Pango.SCALE)
        self.__terminal.set_font(font)
        self.__terminal.set_cursor_blink_mode(Vte.CursorBlinkMode.ON)
        self.__terminal.set_mouse_autohide(True)
        self.__terminal.set_input_enabled(False)
        self.__terminal.connect("child-exited", self.__on_child_exited)
        self.console_box.append(self.__terminal)

        self.__setup_terminal_colors()
        self.console_button.connect("clicked", self.__on_console_toggle)
        self.__console_visible = False

        style_manager = Adw.StyleManager.get_default()
        style_manager.connect("notify::dark", lambda *a: self.__setup_terminal_colors())

    def __setup_terminal_colors(self):
        palette_hex = [
            "#363636", "#c01c28", "#26a269", "#a2734c",
            "#12488b", "#a347ba", "#2aa1b3", "#cfcfcf",
            "#5d5d5d", "#f66151", "#33d17a", "#e9ad0c",
            "#2a7bde", "#c061cb", "#33c7de", "#ffffff",
        ]
        is_dark = Adw.StyleManager.get_default().get_dark()
        fg = Gdk.RGBA()
        bg = Gdk.RGBA()
        fg.parse(palette_hex[15] if is_dark else palette_hex[0])
        bg.parse(palette_hex[0] if is_dark else palette_hex[15])
        colors = [Gdk.RGBA() for _ in palette_hex]
        for color, s in zip(colors, palette_hex):
            color.parse(s)
        self.__terminal.set_colors(fg, bg, colors)

    def __on_console_toggle(self, *args):
        self.__console_visible = not self.__console_visible
        self.console_box.set_visible(self.__console_visible)
        self.console_button.set_label(
            _("Hide Console") if self.__console_visible else _("Show Console")
        )

    def __on_child_exited(self, terminal, status, *args):
        success = (status == 0)
        self.__window.installation_finished(success)

    def start(self, recipe_path: str):
        self.progressbar.pulse()
        self.status_label.set_label(_("Installing TunaOS…"))

        GLib.timeout_add(300, self.__pulse_progress)

        self.__terminal.spawn_async(
            Vte.PtyFlags.DEFAULT,
            "/",
            ["sh", "-c", f"sudo {FISHERMAN_BIN} {recipe_path}; exit $?"],
            [],
            GLib.SpawnFlags.DO_NOT_REAP_CHILD,
            None,
            None,
            -1,
            None,
            None,
        )

    def __pulse_progress(self):
        self.progressbar.pulse()
        return True  # keep pulsing

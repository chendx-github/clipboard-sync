#!/usr/bin/env python3
"""GTK clipboard owner for clipboard-sync (Linux).

Older file managers (Nautilus 3.x on RHEL/Rocky 8) only enable Paste when a
GTK program owns the CLIPBOARD selection. This helper takes ownership with a
Nautilus-compatible file-copy text payload and stays alive until another app
takes ownership (or a safety timeout elapses), so the file manager can paste.

Payload is read from the file path given as argv[1]; it must be text in the
"x-special/nautilus-clipboard" format produced by the Go agent.
"""
import os
import sys
import time
import gi

gi.require_version("Gtk", "3.0")
gi.require_version("Gdk", "3.0")
gi.require_version("GLib", "2.0")
from gi.repository import Gtk, Gdk, GLib


def main():
    if len(sys.argv) < 2:
        sys.exit(1)
    payload_path = sys.argv[1]
    with open(payload_path, "r", encoding="utf-8") as handle:
        payload = handle.read()
    try:
        os.unlink(payload_path)
    except OSError:
        pass

    clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)
    clipboard.set_text(payload, -1)
    clipboard.store()

    started = time.time()

    def on_owner_change(*_):
        # Ignore the ownership change triggered by our own set_text (delivered
        # right after we connect) and quit only when someone else takes over.
        if time.time() - started > 1.0:
            Gtk.main_quit()

    clipboard.connect("owner-change", on_owner_change)
    # Safety net: never stay alive longer than one hour.
    GLib.timeout_add_seconds(3600, Gtk.main_quit)

    Gtk.main()


if __name__ == "__main__":
    main()

# dmgbuild settings for Nexus.dmg (scripts/macos/package-macos.sh).
#
# dmgbuild writes the window layout (.DS_Store: size, background, icon
# positions, icon size) directly, without scripting Finder, so it works the
# same on a headless CI runner. Positions match scripts/macos/dmg-background.swift.
#
#   dmgbuild -s dmg-settings.py -D app=.../Nexus.app -D background=.../background.tiff \
#            -D icon=.../icon.icns "Nexus" Nexus.dmg
import os.path

app = defines["app"]  # noqa: F821 (dmgbuild provides `defines`)
app_name = os.path.basename(app)

format = "UDZO"
compression_level = 9
filesystem = "HFS+"
size = None

files = [app]
symlinks = {"Applications": "/Applications"}
icon = defines.get("icon") or None  # noqa: F821  volume icon
background = defines["background"]  # noqa: F821

show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
sidebar_width = 0

# ((x, y), (width, height)) of the window; the content area is 660x400.
window_rect = ((200, 140), (660, 422))
default_view = "icon-view"
show_icon_preview = False
include_icon_view_settings = True
include_list_view_settings = False

arrange_by = None
grid_offset = (0, 0)
grid_spacing = 100
scroll_position = (0, 0)
label_pos = "bottom"
text_size = 13
icon_size = 112

icon_locations = {
    app_name: (170, 196),
    "Applications": (490, 196),
}
hide_extensions = [app_name]

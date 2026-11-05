"""Repair obsolete XFCE defaults before its settings daemon starts.

Keep personal layout and settings; only migrate removed applications and remove
session-ending UI controls. Workspace lifecycle belongs to Open WebUI. This is
a UI guard, not a security boundary against the workspace's sudo-capable user.
"""
import configparser
import os
from pathlib import Path
import shutil
import sys
import uuid
import xml.etree.ElementTree as ET


REPLACEMENTS = {
    "firefox-esr.desktop": "chromium.desktop",
    "firefox.desktop": "chromium.desktop",
    "org.xfce.ristretto.desktop": "chromium.desktop",
    "htop.desktop": "btop.desktop",
    "xfce4-mail-reader.desktop": "libreoffice-startcenter.desktop",
}


def preserve_invalid(path):
    backup = path.with_name(path.name + ".invalid-" + uuid.uuid4().hex)
    path.rename(backup)
    print("Preserved invalid desktop settings as " + backup.name, file=sys.stderr)


def xml_settings(path, template):
    if path.exists():
        try:
            return ET.parse(path)
        except (ET.ParseError, UnicodeError):
            preserve_invalid(path)
    shutil.copyfile(template, path)
    return ET.parse(path)


def defaults(config):
    config.mkdir(parents=True, exist_ok=True)
    helpers = config / "xfce4/helpers.rc"
    helpers.parent.mkdir(parents=True, exist_ok=True)
    lines = helpers.read_text().splitlines() if helpers.exists() else []
    lines = [line for line in lines if not line.startswith(("WebBrowser=", "FileManager=", "TerminalEmulator="))]
    helpers.write_text("\n".join(lines + ["WebBrowser=chromium", "FileManager=dolphin", "TerminalEmulator=xfce4-terminal"]) + "\n")

    mime = config / "mimeapps.list"
    parser = configparser.ConfigParser(interpolation=None, strict=False)
    parser.optionxform = str
    if mime.exists():
        try:
            parser.read(mime)
        except (configparser.Error, UnicodeError):
            preserve_invalid(mime)
            parser.clear()
            parser.defaults().clear()
    if not parser.has_section("Default Applications"):
        parser.add_section("Default Applications")
    for key, app in {"text/html": "chromium", "x-scheme-handler/http": "chromium",
                     "x-scheme-handler/https": "chromium", "inode/directory": "org.kde.dolphin"}.items():
        parser["Default Applications"][key] = app + ".desktop;"
    with mime.open("w") as target:
        parser.write(target, space_around_delimiters=False)

    channels = config / "xfce4/xfconf/xfce-perchannel-xml"
    channels.mkdir(parents=True, exist_ok=True)
    panel = channels / "xfce4-panel.xml"
    tree = xml_settings(panel, "/etc/xdg/xfce4/panel/default.xml")
    root = tree.getroot()
    removed = set()
    for plugins in root.findall("./property[@name='plugins']"):
        for plugin in list(plugins):
            if plugin.get("value") == "actions":
                removed.add(plugin.get("name").removeprefix("plugin-"))
                plugins.remove(plugin)
            if plugin.get("value") == "whiskermenu":
                for command in ("logout", "logoutuser", "lockscreen", "switchuser", "restart", "shutdown", "suspend", "hibernate"):
                    name = "show-command-" + command
                    prop = plugin.find("./property[@name='" + name + "']")
                    if prop is None:
                        prop = ET.SubElement(plugin, "property", name=name)
                    prop.set("type", "bool")
                    prop.set("value", "false")
    for ids in root.findall(".//property[@name='plugin-ids']"):
        for value in list(ids):
            if value.get("value") in removed:
                ids.remove(value)
    for value in root.iter("value"):
        old = value.get("value")
        if old in REPLACEMENTS:
            value.set("value", REPLACEMENTS[old])
    tree.write(panel, encoding="UTF-8", xml_declaration=True)

    shortcuts = channels / "xfce4-keyboard-shortcuts.xml"
    tree = xml_settings(shortcuts, "/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-keyboard-shortcuts.xml")
    # Missing properties and empty placeholders inherit system defaults. Explicit
    # empty strings in both groups suppress those bindings in partial saved files.
    root = tree.getroot()
    commands = root.find("./property[@name='commands']")
    if commands is None:
        commands = ET.SubElement(root, "property", name="commands", type="empty")
    for name in ("default", "custom"):
        group = commands.find("./property[@name='" + name + "']")
        if group is None:
            group = ET.SubElement(commands, "property", name=name, type="empty")
        for key in ("<Primary><Alt>Delete", "<Primary><Alt>l", "XF86Mail"):
            prop = next((p for p in group if p.get("name") == key), None)
            if prop is None:
                prop = ET.SubElement(group, "property", name=key)
            prop.set("type", "string")
            prop.set("value", "")
    for prop in tree.getroot().iter("property"):
        command = prop.get("value", "")
        if any(part in command for part in ("xfce4-session-logout", "xflock4", "--launch MailReader")):
            prop.set("value", "")
    tree.write(shortcuts, encoding="UTF-8", xml_declaration=True)

    # XFCE copies .desktop entries into launchers with generated file names.
    for launcher in (config / "xfce4/panel").glob("launcher-*/*.desktop"):
        text = launcher.read_text()
        if "--launch MailReader" in text:
            shutil.copyfile("/usr/share/applications/libreoffice-startcenter.desktop", launcher)
        elif any(old in text for old in ("firefox", "ristretto", "Exec=htop")):
            app = "btop" if "Exec=htop" in text else "chromium"
            shutil.copyfile("/usr/share/applications/" + app + ".desktop", launcher)


if __name__ == "__main__":
    defaults(Path(os.environ.get("XDG_CONFIG_HOME", str(Path.home() / ".config"))))

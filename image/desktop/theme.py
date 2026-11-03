"""Recolour the packaged Arc theme without vendoring its source or assets."""
import pathlib
import shutil
import subprocess
import tempfile
import xml.etree.ElementTree as ET

source = pathlib.Path('/usr/share/themes/Arc-Dark')
target = pathlib.Path('/usr/share/themes/Termiplus-Dark')
shutil.copytree(source, target, dirs_exist_ok=True)
replacements = {b'#5294e2': b'#3fb950', b'#73a9e9': b'#63ca72', b'#2679db': b'#1a7f37'}
for bundle in target.rglob('*.gresource'):
    names = subprocess.check_output(['gresource', 'list', str(bundle)], text=True).splitlines()
    with tempfile.TemporaryDirectory() as temp:
        root = pathlib.Path(temp)
        xml = ET.Element('gresources')
        resource = ET.SubElement(xml, 'gresource', prefix='/')
        for name in names:
            data = subprocess.check_output(['gresource', 'extract', str(bundle), name])
            if name.endswith('.css'):
                for old, new in replacements.items():
                    data = data.replace(old, new)
            local = root / name.lstrip('/')
            local.parent.mkdir(parents=True, exist_ok=True)
            local.write_bytes(data)
            ET.SubElement(resource, 'file').text = name.lstrip('/')
        manifest = root / 'resources.xml'
        ET.ElementTree(xml).write(manifest)
        subprocess.run(['glib-compile-resources', str(manifest), '--sourcedir', str(root), '--target', str(bundle)], check=True)
for css in target.rglob('*.css'):
    data = css.read_bytes()
    for old, new in replacements.items():
        data = data.replace(old, new)
    css.write_bytes(data)

# Qt6ct reads its per-user config, seeded before any Qt app starts.
palette = pathlib.Path('/usr/share/qt6ct/colors/darker.conf').read_text()
for old, new in {'#ff424245':'#ff282c30', '#ff12608a':'#ff1a7f37', '#ff0986d3':'#ff3fb950'}.items():
    palette = palette.replace(old, new)
pathlib.Path('/usr/share/qt6ct/colors/termiplus.conf').write_text(palette)

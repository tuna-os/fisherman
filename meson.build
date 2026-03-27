project('tuna-installer', version: '0.1.0', license: 'GPL-3.0-only')

pkgdatadir = join_paths(get_option('prefix'), get_option('datadir'), meson.project_name())
moduledir = join_paths(pkgdatadir, 'tuna_installer')
bindir = join_paths(get_option('prefix'), get_option('bindir'))

gnome = import('gnome')
python = import('python')
py_installation = python.find_installation('python3')

# Build GResources
gnome.compile_resources(
  'tuna-installer',
  'tuna_installer/tuna-installer.gresource.xml',
  source_dir: 'tuna_installer',
  gresource_bundle: true,
  install: true,
  install_dir: pkgdatadir,
)

# Build fisherman Go binary
go = find_program('go', required: true)
fisherman_build = custom_target(
  'fisherman',
  output: 'fisherman',
  input: [],
  command: [
    go, 'build',
    '-C', meson.project_source_root() / 'fisherman',
    '-o', '@OUTPUT@',
    './cmd/fisherman',
  ],
  install: true,
  install_dir: bindir,
  build_by_default: true,
)

# Python launcher script
conf = configuration_data()
conf.set('PYTHON', py_installation.path())
conf.set('VERSION', meson.project_version())
conf.set('pkgdatadir', pkgdatadir)
conf.set('localedir', join_paths(get_option('prefix'), get_option('localedir')))

configure_file(
  input: 'tuna-installer.in',
  output: 'tuna-installer',
  configuration: conf,
  install: true,
  install_dir: bindir,
  install_mode: 'rwxr-xr-x',
)

# Install Python sources
install_data(
  [
    'tuna_installer/__init__.py',
    'tuna_installer/main.py',
    'tuna_installer/window.py',
  ],
  install_dir: moduledir,
)

install_data(
  [
    'tuna_installer/views/__init__.py',
    'tuna_installer/views/welcome.py',
    'tuna_installer/views/disk.py',
    'tuna_installer/views/confirm.py',
    'tuna_installer/views/progress.py',
    'tuna_installer/views/done.py',
  ],
  install_dir: moduledir / 'views',
)

# Desktop entry
install_data(
  'data/org.tunaos.Installer.desktop',
  install_dir: join_paths(get_option('datadir'), 'applications'),
)

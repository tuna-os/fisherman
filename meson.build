go = find_program('go', required: false)

if go.found()
  custom_target(
    'fisherman',
    output: 'fisherman',
    command: [
      go, 'build',
      '-C', meson.current_source_dir() / 'fisherman',
      '-o', meson.current_build_dir() / 'fisherman',
      './cmd/fisherman',
    ],
    build_by_default: true,
    install: true,
    install_dir: get_option('bindir'),
    env: {
      'GOPATH': meson.current_build_dir() / 'gopath',
      'GOCACHE': meson.current_build_dir() / 'gocache',
      'GOPROXY': 'off',
    },
  )
else
  warning('go not found — fisherman binary will not be built')
endif

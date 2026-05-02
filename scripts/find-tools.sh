#!/bin/bash
# find-tools.sh - Dynamic executable discovery for CI and local environments
# Sources this file to get helper functions for finding tools

# Find an executable in common locations
# Usage: find_executable TOOL [FALLBACK1] [FALLBACK2] ...
find_executable() {
  local tool="$1"
  shift
  
  # First try: direct which command
  if command -v "$tool" >/dev/null 2>&1; then
    command -v "$tool"
    return 0
  fi
  
  # Then try fallback paths
  while [ $# -gt 0 ]; do
    if [ -x "$1" ]; then
      echo "$1"
      return 0
    fi
    shift
  done
  
  return 1
}

# Find QEMU executable
find_qemu() {
  find_executable "qemu-system-x86_64" \
    /usr/libexec/qemu-kvm \
    /usr/bin/qemu-system-x86_64 \
    /usr/local/bin/qemu-system-x86_64 || return 1
}

# Find OVMF firmware files
find_ovmf() {
  local code_file ovmf_vars
  
  # Try common locations for OVMF_CODE
  for dir in /usr/share/edk2/ovmf /usr/share/OVMF /usr/share/qemu; do
    if [ -f "$dir/OVMF_CODE.fd" ]; then
      code_file="$dir/OVMF_CODE.fd"
      ovmf_vars="$dir/OVMF_VARS.fd"
      
      if [ -f "$ovmf_vars" ]; then
        echo "$code_file:$ovmf_vars"
        return 0
      fi
    fi
  done
  
  # Try with _4M variant
  for dir in /usr/share/edk2/ovmf /usr/share/OVMF /usr/share/qemu; do
    if [ -f "$dir/OVMF_CODE_4M.fd" ]; then
      code_file="$dir/OVMF_CODE_4M.fd"
      ovmf_vars="$dir/OVMF_VARS_4M.fd"
      
      if [ -f "$ovmf_vars" ]; then
        echo "$code_file:$ovmf_vars"
        return 0
      fi
    fi
  done
  
  return 1
}

# Find SSH executable
find_ssh() {
  find_executable "ssh" \
    /usr/bin/ssh \
    /usr/local/bin/ssh || return 1
}

# Find podman executable
find_podman() {
  find_executable "podman" \
    /usr/bin/podman \
    /usr/local/bin/podman \
    ~/.local/bin/podman || return 1
}

# Find sudo executable
find_sudo() {
  # sudo is usually required, so use it if available
  find_executable "sudo" \
    /usr/bin/sudo \
    /usr/local/bin/sudo || return 1
}

# Find losetup executable
find_losetup() {
  find_executable "losetup" \
    /sbin/losetup \
    /usr/sbin/losetup \
    /usr/bin/losetup || return 1
}

# Find mount executable
find_mount() {
  find_executable "mount" \
    /bin/mount \
    /usr/bin/mount || return 1
}

# Print diagnostic info
show_tool_info() {
  echo "=== Tool Information ==="
  echo "QEMU: $(find_qemu 2>/dev/null || echo 'NOT FOUND')"
  echo "OVMF: $(find_ovmf 2>/dev/null || echo 'NOT FOUND')"
  echo "SSH: $(find_ssh 2>/dev/null || echo 'NOT FOUND')"
  echo "Podman: $(find_podman 2>/dev/null || echo 'NOT FOUND')"
  echo "Sudo: $(find_sudo 2>/dev/null || echo 'NOT FOUND')"
  echo "Losetup: $(find_losetup 2>/dev/null || echo 'NOT FOUND')"
  echo "Mount: $(find_mount 2>/dev/null || echo 'NOT FOUND')"
}

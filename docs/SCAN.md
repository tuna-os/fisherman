# `fisherman scan <disk>`

`fisherman scan` inventories Windows user data that can be offered for migration
before an install. It reads the disk layout, mounts discovered NTFS partitions
read-only, and counts files in known user-profile directories. It does **not**
copy or extract data.

## Prerequisites

- Run as root. The command invokes `lsblk`, `mount`, and `umount`.
- The host must support at least one NTFS filesystem driver. fisherman tries the
  kernel `ntfs3` driver first, then the userspace `ntfs-3g` driver.
- Pass a whole disk, such as `/dev/nvme0n1`, not an individual partition.

Example:

```console
$ sudo fisherman scan /dev/nvme0n1
```

The scan creates a temporary mount point at `/run/fisherman-slurp-scan`,
mounts each candidate partition with `ro,noatime`, and unmounts it before moving
to the next partition. The source filesystem is never mounted writable.

## Output

Successful scans print one indented JSON object to stdout:

```json
{
  "disk": "/dev/sda",
  "partitions": [
    {
      "partition": "/dev/sda2",
      "users": [
        {
          "name": "Alice",
          "categories": [
            {
              "name": "Documents",
              "path": "Documents",
              "bytes": 1024,
              "count": 1
            }
          ],
          "totalBytes": 1024
        }
      ],
      "totalBytes": 1024
    }
  ]
}
```

The output has no `protocol_version` field. Consumers should use the field names and nesting shown here.

Fields:

| Field | Meaning |
|---|---|
| `disk` | Disk argument passed to the command. |
| `partitions` | NTFS partitions that were readable and contained at least one user with non-zero data. |
| `partitions[].partition` | NTFS partition device path. |
| `partitions[].users` | Windows profiles with at least one non-empty category. Built-in profiles are excluded. |
| `partitions[].users[].name` | Windows profile directory name. |
| `partitions[].users[].categories` | Non-empty categories found under that profile. |
| `partitions[].users[].categories[].name` | Display name of the category. |
| `partitions[].users[].categories[].path` | Path relative to the Windows profile directory. |
| `partitions[].users[].categories[].bytes` | Total size of regular files found below the category directory. |
| `partitions[].users[].categories[].count` | Number of regular files found below the category directory. |
| `partitions[].users[].totalBytes` | Sum of that user's category byte counts. |
| `partitions[].totalBytes` | Sum of that partition's user byte counts. |


The current categories and profile-relative paths are:

| Name | Path |
|---|---|
| Documents | `Documents` |
| Desktop | `Desktop` |
| Downloads | `Downloads` |
| Pictures | `Pictures` |
| Music | `Music` |
| Videos | `Videos` |
| Wallpapers | `AppData/Roaming/Microsoft/Windows/Themes` |

`Public`, `Default`, `Default User`, `All Users`, `desktop.ini`, hidden profile
directories, empty categories, and profiles with no counted files are not
reported.

## Empty and unreadable sources

- A disk with no NTFS partitions is not an error. The command exits `0` and
  returns the disk with no discovered partitions (`"partitions": null` in the
  current JSON encoding).
- A partition that cannot be mounted, has no `Users` directory, or has no
  non-empty user profile is omitted. Other partitions continue to be scanned.
- A failure from `lsblk`, creation of the temporary mount point, or a missing
  disk argument exits non-zero and writes the error to stderr instead of
  returning a scan result.

The scan only measures available data. A frontend must perform any later
selection and extraction through the data-migration path before partitioning
the source disk; `scan` itself does not provide a backup or recovery copy.

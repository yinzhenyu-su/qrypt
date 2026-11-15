# Quark offline capture — 2026-11-12

Real provider captures for `improve-offline-fetch` tasks 1.1–1.5. Source was the
Blender open movie **Sintel** (CC BY 3.0):

```
magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel
```

Mount: `quark-all` (Quark account root). Test parse directory:
`/__qrypt_offline_capture_20261112`. No other account entries were modified.
Raw responses were captured with a temporary, uncommitted hook; no tokens,
cookies, or signed URLs are reproduced here.

## 1. Parse response (`POST /offline/download/parse`)

- `title` = `Sintel`
- `total_size` = `129302391`
- `files[]` = 11 entries (the video, `poster.jpg`, and 9 subtitles)
- `resource_info.download_task_finish` = `1` (instant/cached)
- `finished` = `true`, `async_saving` = `false`

Per file:

| field | observed value (example) | meaning |
|---|---|---|
| `path` | `Sintel.de.srt` | **torrent-relative** file path. It does **not** include the `torrent_name` folder prefix. |
| `torrent_name` | `Sintel` | The resource folder name the provider stages into. |
| `file_dirs` | `["Sintel.de.srt"]` | A one-element list containing the same torrent-relative file path; for a root-level file it does **not** include `torrent_name`. Not usable as a folder path by itself. |
| `file_name` | `Sintel.de.srt` | Display file name. |
| `size` | `1652` | File size. |

`path` is therefore relative to the resource root (`torrent_name`), not to the
cloud parse destination and not cloud-absolute.

## 2. Submit response (`POST /offline/download/submit`)

Observed fields:

| field | observed value | meaning |
|---|---|---|
| `task_id` | `53add7d7d5194866a50b509bda787da9` | Provider task id. |
| `dir_fid` | `5f4d86fb5f6b426cba7191dcd83aecfb` | **The resource root folder** (`Sintel`) fid. |
| `fid` | absent | The submit response does not return a top-level `fid`. |
| `files` | same 11 parse entries | Provider repeats the parsed list, still torrent-relative. |
| `finished`, `async_saving` | `true`, `false` | Task already complete. |

The `Sintel` folder fid matched `dir_fid`, and its parent was the parse
directory (`/file/sort` `pdir_fid`). A single-file source still gets a resource
folder for this torrent-shaped source; the folder is the walk-down root.

## 3. Completed task observation (`offdl_result.list`)

The completed observation returned:

```
result_files = [{ name: "Sintel.mp4", size: 129241752, ... }]
```

Only **1 of the 11** staged files was reported. `poster.jpg` and all 9 subtitle
files were omitted, even though the provider task had `finished=true` and the
staged folder contains 11 entries. The task snapshot's `items_done` was 1.

This confirms that `offdl_result.list` cannot be used as the staged layout.

## 4. `/file/sort` and walk-down

- Listing the parse directory returned one child: `Sintel`, with
  `fid=5f4d86fb...`, `pdir_fid=b102e1d8...` (the parse directory),
  `dir=true`, `include_items=11`.
- Listing `Sintel` returned all 11 files; each entry exposes `fid`,
  `pdir_fid=5f4d86fb...`, `file_name`, `dir`, `file_type`, and `size`.
- Entries expose `pdir_fid`, and subdirectories can be listed by their `fid`.
  The walk-down from the output root to the staged files is feasible.

## 5. Reconciliation with design D1/D3

- **D1 (walk the staged tree) remains correct.** One correction: the resource
  root fid comes from submit **`dir_fid`**, not `fid` (submit has no `fid`).
  The current implementation already stores this as `parse_remote_id`/layout
  root.
- **Parse `path`/`file_dirs` are not a complete layout source.** They omit the
  `torrent_name` folder prefix, so using them would require joining
  `torrent_name + path`. This stays a fallback only, not the source of truth.
- **D3 (gate on availability, not a create-time fid) is confirmed.** The
  completed observation can omit most staged files; layout must come from
  listing the output root, and availability must not be inferred from
  `offdl_result.list`.
- **Open question:** Sintel is flat (all files at the torrent root), so nested
  `path` components for a torrent with subdirectories were not captured. The
  provider-neutral walk-down does not depend on them.

## Cleanup

The test directory `/__qrypt_offline_capture_20261112` and its `Sintel` folder
were removed. The mount root listing returned to its pre-test count (16 → 15
entries after removing only the test directory). No other entries were
created, modified, or deleted.

## 6. Post-fix verification (2026-11-12)

After the root/availability fixes:

- `offline add` of Sintel reported `provider_root_id` = the Sintel folder,
  `available=true`, and **11/11 result files** with `Sintel/...` paths.
- `offline show` reported `items_done=11, items_total=11`.
- `offline fetch --download-to /tmp/sintel-fixed/` wrote all 11 files under
  `Sintel/` (total 129302391 bytes) and reported
  `transfer_bytes_done=transfer_bytes_total=129302391`, `items_done=11`.
- The default cloud cleanup removed the 11 staged files and set
  `cleanup_done=true`.
- **Folder cleanup:** the first run left an empty `Sintel/` folder because the
  cleanup's three 300 ms empty checks saw stale entries. After extending the
  folder verify window, a second real fetch removed the `Sintel/` folder as
  well, leaving only the (now empty) parse destination for manual cleanup.

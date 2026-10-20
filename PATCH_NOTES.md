# Local OpenList build notes

This fork keeps the following local features on top of the upstream
`OpenListTeam/OpenList` source tree:

- RAW photo preview (embedded JPEG extraction, thumbnails, content negotiation).
- Media access logging (`[媒体访问] ...`).
- 115 / PikPak cloud download support (driver endpoints + frontend modals).
- Built-in global beautification with a China-friendly live2d 看板娘 loader.
- 115 / 115open listing order and 115 QR-code login helpers.
- Mobile player and batch-download fixes baked into the embedded frontend.

The base source is the latest upstream `main` tree; the fork changes are applied
on top of it and live in the files listed below.

## Embedded frontend

The fork embeds the locally rebuilt OpenList frontend in `public/dist` (it is
committed, see `.gitignore`). `public/public.go` embeds it with `//go:embed all:dist`,
and `build.sh` skips downloading the official frontend when `USE_EMBEDDED_WEB=1`.

Frontend behavior changes included in that dist:

- Mobile toolbar hides ArtPlayer web-fullscreen (`fullscreenWeb`) and keeps native fullscreen.
- Mobile progress touch area is reduced and ignores toolbar controls, preventing fullscreen taps from being treated as progress seeks.
- Mobile progress handling also ignores ArtPlayer settings panels, so subtitle/subtitle-offset rows can be tapped without being captured as progress seeks.
- DPlayer-style seeking remains: dragging previews only; one real seek is sent on release.
- Mobile video-surface horizontal gesture seeking is preserved.
- Batch download no longer relies on `window.open` (which only lets the first file through), and the 115 / PikPak cloud download modals are available from the toolbar.

## Backend changes

- Media preview/download/player access is logged with `[媒体访问] 时间：...`; one
  access produces one line (`server/common/access_log.go`), and `/api/fs/get`,
  `/d/`, `/p/` and `/sd/` are deduplicated into a single entry.
- RAW photos are previewed from the JPEG rendition embedded in the container
  (`internal/rawpreview`, `internal/op/rawpreview.go`, `server/common/rawpreview.go`,
  `server/handles/rawpreview.go`). Settings: `raw_preview_enabled`,
  `raw_preview_types`, `raw_preview_negotiate`, `raw_preview_thumb_size`,
  `raw_preview_max_size`, `raw_preview_cache_size`; the RAW extensions are folded
  into `image_types` while extraction is on.
- 115 cloud download helpers: cover generation (`drivers/115/cover.go`) and the
  QR-code login endpoints (`server/handles/qrcode_115.go`, routed at
  `/api/admin/setting/get_115_qrcode` and `/api/admin/setting/check_115_qrcode`).
- PikPak cloud download: `drivers/pikpak/cloud_download.go` adds the
  `pikpak_cloud_download_list` / `_add` / `_delete` / `_clear` driver methods used
  by the modal, operating on the currently open directory.
- 115 and 115open default directory listing is name ascending.
- MCP `fs/get` and `fs/list` return the same thumbnail URLs as the HTTP API.

## Built-in global beautification (source-integrated)

The global custom content previously injected through the `customize_head` /
`customize_body` settings (web font, FontAwesome icons, live2d-widget 看板娘,
background images, transparency / frosted-glass CSS) is now built into the backend
in `server/static/builtin_customize.go`:

- It is always injected into normal pages (and share pages) at startup and whenever settings are saved, independent of the database settings.
- `/@manage` pages do not receive it, matching the previous settings behavior.
- Content still entered in the `customize_head` / `customize_body` settings is appended after the built-in block; clear both fields to avoid duplicate script/style loading.
- The live2d 看板娘 loader now prefers China-friendly jsdelivr mirrors (`cdn.jsdmirror.com` → `jsd.onmicrosoft.cn` → `fastly.jsdelivr.net` fallback) for both widget assets and the model library, replacing the slow fastly-only chain.

## CI

GitHub Actions workflow `.github/workflows/build-all-embedded-dist.yml` builds
binaries with `USE_EMBEDDED_WEB=1`, so `build.sh` uses the committed `public/dist`
instead of downloading official frontend assets. Compressed binaries are uploaded
as per-platform artifacts.

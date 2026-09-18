# Local OpenList build notes

This fork embeds a locally rebuilt OpenList frontend in `public/dist`.

Frontend behavior changes included in that dist:

- Mobile toolbar hides ArtPlayer web-fullscreen (`fullscreenWeb`) and keeps native fullscreen.
- Mobile progress touch area is reduced and ignores toolbar controls, preventing fullscreen taps from being treated as progress seeks.
- Mobile progress handling also ignores ArtPlayer settings panels, so subtitle/subtitle-offset rows can be tapped without being captured as progress seeks.
- DPlayer-style seeking remains: dragging previews only; one real seek is sent on release.
- Mobile video-surface horizontal gesture seeking is preserved.

Backend behavior changes:

- Media preview/download/player access is logged with `[媒体访问] 时间：...`.
- 115 and 115open default directory listing is name ascending.

## Built-in global beautification (source-integrated)

The global custom content previously injected through the `customize_head` / `customize_body` settings (web font, FontAwesome icons, live2d-widget 看板娘, background images, transparency / frosted-glass CSS) is now built into the backend in `server/static/builtin_customize.go`:

- It is always injected into normal pages (and share pages) at startup and whenever settings are saved, independent of the database settings.
- `/@manage` pages do not receive it, matching the previous settings behavior.
- Content still entered in the `customize_head` / `customize_body` settings is appended after the built-in block; clear both fields to avoid duplicate script/style loading.
- The live2d 看板娘 loader now prefers China-friendly jsdelivr mirrors (`cdn.jsdmirror.com` → `jsd.onmicrosoft.cn` → `fastly.jsdelivr.net` fallback) for both widget assets and the model library, replacing the slow fastly-only chain.

GitHub Actions workflow `.github/workflows/build-all-embedded-dist.yml` builds binaries with `USE_EMBEDDED_WEB=1`, so `build.sh` uses the committed `public/dist` instead of downloading official frontend assets.

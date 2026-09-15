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

GitHub Actions workflow `.github/workflows/build-all-embedded-dist.yml` builds binaries with `USE_EMBEDDED_WEB=1`, so `build.sh` uses the committed `public/dist` instead of downloading official frontend assets.

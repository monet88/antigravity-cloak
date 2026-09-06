# Changelog

All notable changes to `antigravity-cloak` are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Current releases use date-based CalVer (`YY.MM.DD`) with Git tags in the `v.YY.MM.DD` form; older releases used SemVer.

## [Unreleased]

## [26.09.06] - 2026-09-06

### Changed
- Switched release numbering from SemVer to date-based CalVer (`YY.MM.DD`), with Git tags using the `v.YY.MM.DD` form.

## [0.4.3] - 2026-09-02

### Fixed
- Preserved only the exact dot-prefixed `.omp` filesystem path segment during forward OMP brand rewriting, while continuing to mask bare `omp`, `/omp/`, `\omp\`, `.omp-backup`, `profile.omp`, and other brand aliases.

### Removed
- Removed obsolete Claude-specific repository guidance after retiring that workflow.

## [0.4.2] - 2026-09-01

### Added
- Added deterministic `X-Cloak-Client` request identity override with control-header consumption so the header never leaks upstream.
- Added conservative positive User-Agent evidence for thin Oh My Pi requests while preserving body-based classification as fallback.
- Added Oh My Pi response identity restoration: assistant-visible standalone `Antigravity` text is rewritten back to `omp` without touching tool arguments or unrelated data.

### Fixed
- Preserved invalid explicit-client precedence across response and stream paths so weaker UA evidence cannot override a bad explicit signal.
- Hardened reverse-brand streaming across OpenAI and Anthropic terminal events, fragmented semantic lanes, singleton content maps, and multi-choice completion.
- Ensured Anthropic standalone terminal flushes keep correct data framing.

## [0.4.1] - 2026-08-29

### Added
- Added ADR 0002 for ratio-ranked client classification and request/session pre-registration.
- Added an offline integration/lifecycle test suite and Python CI validation.

### Changed
- Streamlined repository documentation and synchronized GitNexus guidance/statistics.

## [0.4.0] - 2026-08-27

### Added
- Added Oh My Pi Vibe Mode tool cloaking (`vibe_*`).
- Added Oh My Pi Autoresearch tool cloaking (`init_experiment`, `run_experiment`, `log_experiment`, `update_notes`).

## [0.3.1] - 2026-08-27

### Fixed
- Fixed streamed Oh My Pi tool-call uncloaking so Antigravity tool names are restored correctly at the OMP client boundary.

### Added
- Added integration scripts covering Oh My Pi and streaming client behavior.

## [0.3.0] - 2026-08-27

### Added
- Added Oh My Pi client detection and bidirectional tool-name cloaking.
- Added remote VPS custom-plugin installation documentation.

### Changed
- Synchronized upstream coding-agent brand keywords.

## [0.2.0] - 2026-06-29

### Added
- Added `model_prefixes` gating so cloaking can be restricted to selected model/provider prefixes.

### Fixed
- Fixed client detection behavior to make cloak/uncloak classification more reliable.

## [0.1.1] - 2026-06-28

### Fixed
- Renamed the Go module path to the fork repository path so module metadata matches distribution.

## [0.1.0] - 2026-06-28

### Added
- Added structured tool-name cloaking on requests and symmetric uncloaking on responses/streams.
- Added reload hardening for runtime configuration changes.
- Added plugin store registry metadata for distribution.

## [0.0.3] - 2026-06-20

### Added
- Added request-side rewriting of coding-agent identity signals to `Antigravity`.

## [0.0.2] - 2026-06-19

### Added
- Added configurable brand-rewrite keywords.

## [0.0.1] - 2026-06-19

### Added
- Initial plugin implementation.
- Added repository metadata, MIT license, and release build workflow.

[Unreleased]: https://github.com/monet88/antigravity-cloak/compare/v.26.09.06...HEAD
[26.09.06]: https://github.com/monet88/antigravity-cloak/releases/tag/v.26.09.06
[0.4.3]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.3
[0.4.2]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.2
[0.4.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.1
[0.4.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.4.0
[0.3.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.1
[0.3.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.3.0
[0.2.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.2.0
[0.1.1]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.1
[0.1.0]: https://github.com/monet88/antigravity-cloak/releases/tag/v0.1.0
[0.0.3]: https://github.com/monet88/antigravity-cloak/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/monet88/antigravity-cloak/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/monet88/antigravity-cloak/tree/v0.0.1

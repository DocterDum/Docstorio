// Package mods owns everything about installed mods (DESIGN "Mods"):
//
//   - Version and Dependency parsing, and Compatible, the single owner of
//     the "factorio_version must equal the running major.minor" rule.
//   - Info / ReadInfo: info.json read in memory from a mod zip with the
//     DESIGN "File handling" limits (zips are never extracted).
//   - Builtins: the built-in mods in /opt/factorio/data (excluding core).
//   - Scanner: the mod zips in mods/, cached by (file name, size, mtime).
//   - State: the desired enabled/disabled state (manager/mod-state.json)
//     and RenderModList, the complete mods/mod-list.json written before
//     every launch.
//   - Planner and CheckSet: the dependency planner behind
//     POST /api/portal/plan, install and update. It reads releases through
//     ReleaseSource, which package portal implements; mods never imports
//     portal.
//   - Manager: the facade the API and main use.
//
// mods imports fsutil and jobs (for the Logger interface) only.
package mods

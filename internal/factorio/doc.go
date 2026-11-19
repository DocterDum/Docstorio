// Package factorio runs and manages the Factorio server process and the
// files Factorio owns (DESIGN "Process model", FACTS "Factorio 2.1.24"):
//
//   - Supervisor: the single Factorio child, its State (stopped, starting,
//     running, stopping, failed), Start / Stop / Restart, graceful shutdown,
//     Status snapshots, the effective server-settings file, world creation
//     and the saves API.
//   - LogBuffer: ring of the last 5000 output lines with line numbers, also
//     carrying "[docstorio]" manager events.
//   - RCON: one persistent connection behind a mutex (console, players).
//   - Seed: creation of missing config files at startup.
//   - ConfigFiles: the config file API (raw JSON files and the
//     server-settings form derived from Factorio's example).
//
// The supervisor does not import package mods' Manager: main wires
// Config.WriteModList to mods.Manager.WriteModList. factorio imports mods
// only for the Version type.
//
// factorio imports settings, fsutil, jobs (Logger) and mods (Version).
package factorio

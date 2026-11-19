// Package web embeds the single-page UI (vanilla HTML/CSS/JS, no build step,
// no CDN). The files live in static/; main serves fs.Sub(Static, "static")
// at "/".
package web

import "embed"

// Static holds static/index.html, static/app.js and static/style.css.
//
//go:embed static
var Static embed.FS

package factorio

import "strings"

// ParsePlayersOnline parses the reply of "/players online" ("Online players
// (N):" then one indented name per line, optionally suffixed " (online)")
// into the names. Never nil.
//
// The format with players online is not verified (FACTS), so parsing is
// lenient: "\r\n" line endings, any indentation and a missing suffix are
// accepted. A reply without the "Online players" header (an error message,
// for example) gives no names.
func ParsePlayersOnline(reply string) []string {
	names := []string{}
	header := false
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !header {
			header = strings.HasPrefix(strings.ToLower(line), "online players")
			if !header {
				return names
			}
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(line, "(online)"))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

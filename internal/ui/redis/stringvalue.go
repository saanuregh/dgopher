package redis

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/safety"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// binaryNote says why a string shows as hex.
const binaryNote = "Not UTF-8: shown as hex, read only."

// isBinary reports whether a string's bytes are not UTF-8, which a text
// area would edit as other bytes. Shown in part, its start may end
// within a character.
func isBinary(value string, whole bool) bool {
	if !whole {
		for i := 0; i < utf8.UTFMax && len(value) > 0; i++ {
			if utf8.ValidString(value) {
				return false
			}
			value = value[:len(value)-1]
		}
	}
	return !utf8.ValidString(value)
}

// setStringValue keeps a string read, and its hex when it is binary.
func (r *Tab) setStringValue(value string, whole bool) {
	r.value, r.whole = value, whole
	r.editValue, r.editDirty = widgets.TextStart(value, stringStart), false
	r.binaryHex = ""
	if isBinary(value, whole) {
		// The first stringStart bytes dump to more than the 4*stringStart
		// shown, so the rest need not be dumped.
		r.binaryHex = widgets.TextStart(hex.Dump([]byte(value[:min(len(value), stringStart)])), 4*stringStart)
	}
}

// binaryView shows a binary string's hex, read only.
func (r *Tab) binaryView(c *ui.Context, loadAll func(c *ui.Context)) {
	pal := widgets.PaletteOf(c)
	area := &r.valueArea
	area.Label, area.Language, area.ReadOnly = "Value", editor.LanguagePlain, true
	el, _ := area.View(c, &r.binaryHex)
	el.Grow(1)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, binaryNote).FontSize(12).TextColor(pal.Muted).Shrink(1)
		loadAll(c)
	})
}

// loadWholeAsk is the size of a string above which Load All asks first.
const loadWholeAsk = 10 << 20

// askLoadWhole reads all of a string shown in part, asking first when it
// is large.
func (r *Tab) askLoadWhole() {
	if r.info.Length <= loadWholeAsk {
		r.loadWhole()
		return
	}
	size := widgets.HumanBytes(r.info.Length)
	v := safety.Verdict{Confirm: true, Reasons: []string{"The app reads all " + size + " of the value and keeps it in memory."}}
	r.a.AskConfirm(r.conn, v, "Load all "+size+" of "+r.selected+"?", "Load All", "GET "+r.selected, r.loadWhole).Harmless = true
}

// saveString writes a string's new value, keeping its time to live: by
// KEEPTTL from Redis 6, and before by a script that sets it again.
func (r *Tab) saveString(key, value string) {
	if r.serverVersion != "" {
		r.write(setKeepingTTL(key, value, r.serverVersion), r.loadKey)
		return
	}
	kv := r.conn.KV()
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		version := kv.ServerVersion(ctx)
		return func() {
			r.serverVersion = version
			r.write(setKeepingTTL(key, value, version), r.loadKey)
		}
	})
}

// keepTTLScript sets a string, then its time to live again, as KEEPTTL
// does, which Redis before 6 lacks.
const keepTTLScript = `local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 then return redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl) end
return redis.call('SET', KEYS[1], ARGV[1])`

// setKeepingTTL is the command that sets a string and keeps its time to
// live on a server of version; KEEPTTL when the version is unknown.
func setKeepingTTL(key, value, version string) []string {
	major, _, _ := strings.Cut(version, ".")
	if n, err := strconv.Atoi(major); err == nil && n < 6 {
		return []string{"EVAL", keepTTLScript, "1", key, value}
	}
	return []string{"SET", key, value, "KEEPTTL"}
}

// ttlInvalid says how a key's TTL is typed.
const ttlInvalid = "TTL is whole seconds: empty removes it."

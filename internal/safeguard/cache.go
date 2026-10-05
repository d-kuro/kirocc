package safeguard

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/d-kuro/kirocc/internal/respconv"
)

// verdictCache memoises Jev verdicts so a tool call already judged this session
// is not re-judged. Only confident verdicts are stored (NotFlagged, Flagged);
// Skip is never cached -- it means "could not judge", and must stay retryable
// next turn. Entries expire after ttl and the map is bounded by maxEntries.
//
// The key is the crux. When Jev says its verdict generalises across arguments
// (a read like `grep` or `cat`, whose safety does not depend on which file),
// the key is the command *shape* so every argument variation shares one entry.
// When Jev says the verdict depends on the arguments (an `rm`, a `git push`, a
// `curl` carrying data), the key is the exact input, so a benign-args verdict
// can never be reused for dangerous args. kirocc never decides which is which;
// Jev's `generalizable` answer does.
type verdictCache struct {
	ttl        time.Duration
	maxEntries int

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	status  respconv.SafeguardStatus
	expires time.Time
}

func newVerdictCache(ttl time.Duration, maxEntries int) *verdictCache {
	return &verdictCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]cacheEntry),
	}
}

// get returns a live cached status for the key, if present and unexpired.
func (vc *verdictCache) get(key string) (respconv.SafeguardStatus, bool) {
	if vc == nil {
		return respconv.SafeguardStatus{}, false
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	e, ok := vc.entries[key]
	if !ok {
		return respconv.SafeguardStatus{}, false
	}
	if time.Now().After(e.expires) {
		delete(vc.entries, key)
		return respconv.SafeguardStatus{}, false
	}
	return e.status, true
}

// put stores a confident verdict. Skip is never stored; a Skip caller must not
// reach here. A crude size cap drops the whole map when full -- entries are
// short-lived and cheap to rebuild, so this beats tracking per-entry LRU.
func (vc *verdictCache) put(key string, status respconv.SafeguardStatus) {
	if vc == nil || status.Skip {
		return
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if len(vc.entries) >= vc.maxEntries {
		vc.entries = make(map[string]cacheEntry, vc.maxEntries)
	}
	vc.entries[key] = cacheEntry{status: status, expires: time.Now().Add(vc.ttl)}
}

// cacheKey builds the lookup key for a tool call. generalizable selects the
// command part: the normalised shape when true, the exact input when false.
// Every key also binds the policy, model and thresholds, because the same call
// under a different permission policy or model can warrant a different verdict.
func cacheKey(u ToolUse, policyHash string, model string, t Thresholds, generalizable bool) string {
	h := sha256.New()
	writeField := func(s string) {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	if generalizable {
		writeField("shape")
		writeField(u.Name)
		writeField(commandShape(u))
	} else {
		writeField("literal")
		writeField(u.Name)
		writeField(u.Input)
	}
	writeField(policyHash)
	writeField(model)
	writeField(thresholdsFingerprint(t))
	return hex.EncodeToString(h.Sum(nil))
}

// commandShape reduces a tool call to a structural fingerprint that keeps every
// token which could change the verdict (the verb, all flags, the pipe/redirect
// structure) and abstracts away the value-operands (paths, patterns, messages,
// URLs) that cannot. It is only ever consulted for verdicts Jev marked
// generalizable, so an imperfect shape costs a cache miss, never a wrongly
// shared verdict. For a Bash command it normalises the shell string; for any
// other tool it falls back to the tool name (the whole tool is the shape).
func commandShape(u ToolUse) string {
	if u.Name != "Bash" {
		return u.Name
	}
	cmd := parseBashCommand(u.Input)
	if cmd == "" {
		return u.Input // unparseable: cannot abstract safely, keep literal-ish
	}
	fields := strings.Fields(cmd)
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch {
		case f == "|" || f == "&&" || f == "||" || f == ";" || f == ">" || f == ">>" || f == "<":
			b.WriteString(f) // structure: keep verbatim
		case strings.HasPrefix(f, "-"):
			b.WriteString(f) // a flag: keep verbatim (flags change the verdict)
		case i == 0:
			b.WriteString(f) // the verb
		default:
			b.WriteString("\x00") // an operand: abstract to a placeholder
		}
	}
	return b.String()
}

// thresholdsFingerprint renders the thresholds compactly for the cache key so a
// verdict computed under one threshold set is not reused under another.
func thresholdsFingerprint(t Thresholds) string {
	var b strings.Builder
	for _, v := range []float64{t.GeneralizableCap, t.ChoiceConfidenceFloor} {
		var n [8]byte
		binary.LittleEndian.PutUint64(n[:], uint64(v*1000))
		b.Write(n[:])
	}
	return b.String()
}

// hashPolicy fingerprints the raw policy document so it can bind a cache key
// without storing the whole thing.
func hashPolicy(policy []byte) string {
	if len(policy) == 0 {
		return ""
	}
	sum := sha256.Sum256(policy)
	return hex.EncodeToString(sum[:])
}

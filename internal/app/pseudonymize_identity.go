package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"hash/maphash"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// personIdentity is the stable stand-in for one person. Registered users derive it from
// their ID and everyone else from a keyed hash of their email address, so all of a
// person's rows agree without any table holding the original.
type personIdentity struct {
	userID uint64
	hash   [6]byte
	guest  bool
	kept   bool
}

func (id personIdentity) appendSlug(dst []byte) []byte {
	if id.guest {
		dst = append(dst, "guest-"...)
		return hex.AppendEncode(dst, id.hash[:])
	}
	dst = append(dst, "user-"...)
	return strconv.AppendUint(dst, id.userID, 10)
}

func (id personIdentity) appendFirstName(dst []byte) []byte {
	if id.guest {
		return append(dst, "Guest"...)
	}
	return append(dst, "User"...)
}

func (id personIdentity) appendLastName(dst []byte) []byte {
	if id.guest {
		return hex.AppendEncode(dst, id.hash[:])
	}
	return strconv.AppendUint(dst, id.userID, 10)
}

func (id personIdentity) appendDisplayName(dst []byte) []byte {
	dst = id.appendFirstName(dst)
	dst = append(dst, ' ')
	return id.appendLastName(dst)
}

const (
	identityRefGuest uint64 = 1 << 63
	identityRefKept  uint64 = 1 << 62
)

// ref packs an identity into one word, because a shop's link maps hold one per order.
func (id personIdentity) ref() uint64 {
	if id.guest {
		value := uint64(0)
		for _, b := range id.hash {
			value = value<<8 | uint64(b)
		}
		return value | identityRefGuest
	}
	value := id.userID
	if id.kept {
		value |= identityRefKept
	}
	return value
}

func identityFromRef(ref uint64) personIdentity {
	if ref&identityRefGuest != 0 {
		id := personIdentity{guest: true}
		for i := len(id.hash) - 1; i >= 0; i-- {
			id.hash[i] = byte(ref)
			ref >>= 8
		}
		return id
	}
	return personIdentity{userID: ref &^ identityRefKept, kept: ref&identityRefKept != 0}
}

// identityRegistry resolves rows to identities. The key is generated here and never
// leaves this process, so guest pseudonyms cannot be reversed or linked across two pulls.
//
// The user maps are written while the users table runs alone and only read afterwards.
// The link maps and marks grow while their tables run in parallel, so they take the lock.
type identityRegistry struct {
	key    []byte
	hashes sync.Pool
	domain string
	// emailSuffix is "@domain" in lower case, for recognizing the filter's own output.
	emailSuffix string

	keptIDs     map[uint64]struct{}
	keptEmails  map[string]struct{}
	userByEmail map[string]uint64
	userByLogin map[string]uint64
	users       map[uint64]struct{}

	mu    sync.RWMutex
	links map[string]map[uint64]uint64
	marks map[string]map[uint64]struct{}
}

func newIdentityRegistry(domain string, keptIDs []int) (*identityRegistry, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	registry := &identityRegistry{
		key:         key,
		domain:      domain,
		emailSuffix: "@" + strings.ToLower(domain),
		keptIDs:     make(map[uint64]struct{}, len(keptIDs)),
		keptEmails:  map[string]struct{}{},
		userByEmail: map[string]uint64{},
		userByLogin: map[string]uint64{},
		users:       map[uint64]struct{}{},
		links:       map[string]map[uint64]uint64{},
		marks:       map[string]map[uint64]struct{}{},
	}
	registry.hashes.New = func() any { return hmac.New(sha256.New, key) }
	for _, id := range keptIDs {
		registry.keptIDs[uint64(id)] = struct{}{}
	}
	return registry, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (r *identityRegistry) registerUser(id uint64, email string, login string) {
	r.users[id] = struct{}{}
	if normalized := normalizeEmail(email); normalized != "" {
		r.userByEmail[normalized] = id
		if _, kept := r.keptIDs[id]; kept {
			r.keptEmails[normalized] = struct{}{}
		}
	}
	if login != "" {
		r.userByLogin[login] = id
	}
}

func (r *identityRegistry) forUser(id uint64) personIdentity {
	_, kept := r.keptIDs[id]
	return personIdentity{userID: id, kept: kept}
}

func (r *identityRegistry) isUser(id uint64) bool {
	_, ok := r.users[id]
	return ok
}

// forEmail never keeps: anyone can type a kept user's address into a checkout, and that
// stranger's name and address must not stay real because of it.
func (r *identityRegistry) forEmail(email string) personIdentity {
	normalized := normalizeEmail(email)
	if id, ok := r.userByEmail[normalized]; ok {
		return personIdentity{userID: id}
	}
	return r.guest("email:", normalized)
}

// forRow is the stand-in for a row that names nobody we can resolve. Rows sharing the
// key share it.
func (r *identityRegistry) forRow(key string) personIdentity {
	return r.guest("row:", key)
}

func (r *identityRegistry) forLogin(login string) (personIdentity, bool) {
	id, ok := r.userByLogin[login]
	if !ok {
		return personIdentity{}, false
	}
	return r.forUser(id), true
}

func (r *identityRegistry) remember(linkMap string, id uint64, identity personIdentity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	links := r.links[linkMap]
	if links == nil {
		links = map[uint64]uint64{}
		r.links[linkMap] = links
	}
	links[id] = identity.ref()
}

func (r *identityRegistry) recall(linkMap string, id uint64) (personIdentity, bool) {
	r.mu.RLock()
	ref, ok := r.links[linkMap][id]
	r.mu.RUnlock()
	if !ok {
		return personIdentity{}, false
	}
	return identityFromRef(ref), true
}

func (r *identityRegistry) mark(markMap string, id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	marks := r.marks[markMap]
	if marks == nil {
		marks = map[uint64]struct{}{}
		r.marks[markMap] = marks
	}
	marks[id] = struct{}{}
}

func (r *identityRegistry) isMarked(markMap string, id uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.marks[markMap][id]
	return ok
}

// isOwnAddress recognizes the exact form this filter writes, not the domain alone: a
// project may configure a real domain, and real addresses there must not pass for
// pseudonyms. A kept account's address counts as well, because it stays on purpose.
func (r *identityRegistry) isOwnAddress(email string) bool {
	normalized := normalizeEmail(email)
	if _, kept := r.keptEmails[normalized]; kept {
		return true
	}
	local, ok := strings.CutSuffix(normalized, r.emailSuffix)
	if !ok {
		return false
	}
	if digits, ok := strings.CutPrefix(local, "user-"); ok {
		return isAllDigits(digits)
	}
	if digest, ok := strings.CutPrefix(local, "guest-"); ok {
		return len(digest) == 12 && isAllHex(digest)
	}
	return false
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func isAllHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (r *identityRegistry) sum(prefix string, value string) []byte {
	mac := r.hashes.Get().(hash.Hash)
	mac.Reset()
	mac.Write([]byte(prefix))
	mac.Write([]byte(value))
	sum := mac.Sum(nil)
	r.hashes.Put(mac)
	return sum
}

func (r *identityRegistry) guest(prefix string, value string) personIdentity {
	id := personIdentity{guest: true}
	copy(id.hash[:], r.sum(prefix, value))
	return id
}

// token is a stand-in that stays distinct for distinct inputs, for columns with a unique
// index such as transaction IDs.
func (r *identityRegistry) token(value string) string {
	return hex.EncodeToString(r.sum("token:", value)[:16])
}

func (r *identityRegistry) appendEmail(dst []byte, id personIdentity) []byte {
	dst = id.appendSlug(dst)
	dst = append(dst, '@')
	return append(dst, r.domain...)
}

// nameDictionary finds the known people's full names in free text, such as "order for
// Anna Real" in a note. It stores hashes rather than names, because a large shop has
// millions of customers and a lookup needs no more than the hash.
//
// Workers add to pending under the lock and read the rest without one: the filter merges
// them only at a table boundary, while no worker runs.
type nameDictionary struct {
	seed    maphash.Seed
	entries map[uint64]uint64
	// prefixes says which first words and first two words can start a name, so the scan
	// spends a bit test on each word of a text and looks up whole names only for the few
	// that can. First names are ordinary words often enough for this to matter.
	prefixes []uint64
	// protected holds what looks like a name but belongs to the site, such as a brand.
	protected map[uint64]struct{}

	mu               sync.Mutex
	pending          map[uint64]pendingName
	pendingProtected []uint64
	// partial holds the half of a name that sits in its own row, as user meta and legacy
	// order meta do, until the other half arrives.
	partial map[string]partialName
}

type partialName struct {
	first, last string
}

type pendingName struct {
	ref uint64
	namePrefixes
}

// namePrefixes are the hashes of a name's first word and of its first two.
type namePrefixes struct {
	first, firstTwo uint32
}

const (
	nameMaxWords = 6
	nameMaxBytes = 96
	// A million names set at most a quarter of these, and the set still fits a CPU cache.
	namePrefixBits = 1 << 23
	// A longer gap between two words is layout rather than the space inside a name.
	nameMaxGap = 16
)

func newNameDictionary() *nameDictionary {
	return &nameDictionary{
		seed:      maphash.MakeSeed(),
		entries:   map[uint64]uint64{},
		prefixes:  make([]uint64, namePrefixBits/64),
		protected: map[uint64]struct{}{},
		pending:   map[uint64]pendingName{},
		partial:   map[string]partialName{},
	}
}

// record stores a full name of at least two words, which nameKey ensures. One word alone
// is too common to replace in free text: "Mark" or "Will" would rewrite ordinary sentences.
func (d *nameDictionary) record(key uint64, prefixes namePrefixes, identity personIdentity) {
	d.mu.Lock()
	d.pending[key] = pendingName{ref: identity.ref(), namePrefixes: prefixes}
	d.mu.Unlock()
}

// addPart pairs first and last names that live in separate rows of one object.
func (d *nameDictionary) addPart(object string, value []byte, first bool, identity personIdentity) {
	if identity.kept || len(value) == 0 {
		return
	}
	d.mu.Lock()
	part := d.partial[object]
	if first {
		part.first = string(value)
	} else {
		part.last = string(value)
	}
	if part.first == "" || part.last == "" {
		d.partial[object] = part
		d.mu.Unlock()
		return
	}
	delete(d.partial, object)
	key, prefixes, ok := d.nameKey([]byte(part.first + " " + part.last))
	if ok {
		d.pending[key] = pendingName{ref: identity.ref(), namePrefixes: prefixes}
	}
	d.mu.Unlock()
}

// protect keeps a phrase from counting as a name from the next table on. The person of
// that name still gets a pseudonym in every field that holds it; only free text keeps the
// phrase, because there it cannot be told apart from the brand or site of the same name.
func (d *nameDictionary) protect(phrase []byte) {
	key, _, ok := d.nameKey(phrase)
	if !ok {
		return
	}
	d.mu.Lock()
	d.pendingProtected = append(d.pendingProtected, key)
	d.mu.Unlock()
}

// isProtected may only run while no publish does, which holds for every worker.
func (d *nameDictionary) isProtected(key uint64) bool {
	_, ok := d.protected[key]
	return ok
}

// publish merges what the last tables added. Call it only while no worker runs.
func (d *nameDictionary) publish() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, key := range d.pendingProtected {
		d.protected[key] = struct{}{}
		delete(d.entries, key)
	}
	d.pendingProtected = d.pendingProtected[:0]
	for key, name := range d.pending {
		if _, protected := d.protected[key]; protected {
			continue
		}
		d.entries[key] = name.ref
		d.prefixes[name.first/64] |= 1 << (name.first % 64)
		d.prefixes[name.firstTwo/64] |= 1 << (name.firstTwo % 64)
	}
	clear(d.pending)
}

func (d *nameDictionary) hasPrefix(hash uint32) bool {
	return d.prefixes[hash/64]&(1<<(hash%64)) != 0
}

// nameGapLen returns the length of the white space between two words of a name at i, or 0.
// People's names reach a database with whatever the form or editor made of the space.
func nameGapLen(text []byte, i int) int {
	start := i
	for i < len(text) && i-start <= nameMaxGap {
		switch c := text[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == 0xc2 && i+1 < len(text) && text[i+1] == 0xa0:
			i += 2
		case c == '&' && bytes.HasPrefix(text[i:], []byte("&nbsp;")):
			i += len("&nbsp;")
		default:
			return i - start
		}
	}
	if i-start > nameMaxGap {
		return 0
	}
	return i - start
}

// nameCharAt returns the character at i as a name stores it, and its width, or width 0 when
// it cannot be part of a name. A quotation mark or a dash ends a word as a space does, while
// bytes that are no UTF-8 count as letters, because old data holds Latin-1. Folding keeps the
// width, since a match has to map back to the text.
func nameCharAt(text []byte, i int) (folded [4]byte, width int) {
	c := text[i]
	if c < utf8.RuneSelf {
		if !nameBytes[c] {
			return folded, 0
		}
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		folded[0] = c
		return folded, 1
	}
	r, size := utf8.DecodeRune(text[i:])
	if r == utf8.RuneError && size <= 1 {
		folded[0] = c
		return folded, 1
	}
	if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != typographicApostrophe {
		return folded, 0
	}
	lower := unicode.ToLower(r)
	if utf8.RuneLen(lower) != size {
		lower = r
	}
	utf8.EncodeRune(folded[:], lower)
	return folded, size
}

const typographicApostrophe = '\u2019'

// firstWordHash hashes the word at i in the form a name's first word is stored in.
func firstWordHash(text []byte, i int) uint32 {
	hash := uint32(2166136261)
	// No stored name is longer, and a-b-c-d... has a word start at every other byte.
	for end := min(len(text), i+nameMaxBytes); i < end; {
		// Nearly every byte here is ASCII, and this runs for every word of a text.
		if c := text[i]; c < utf8.RuneSelf {
			if !nameBytes[c] {
				break
			}
			if c >= 'A' && c <= 'Z' {
				c += 32
			}
			hash = (hash ^ uint32(c)) * 16777619
			i++
			continue
		}
		folded, width := nameCharAt(text, i)
		if width == 0 {
			break
		}
		for k := 0; k < width; k++ {
			hash = (hash ^ uint32(folded[k])) * 16777619
		}
		i += width
	}
	return hash % namePrefixBits
}

// normalizedPrefixHash hashes the start of a name that nameKey or a lookup already folded.
func normalizedPrefixHash(name []byte) uint32 {
	hash := uint32(2166136261)
	for _, c := range name {
		hash = (hash ^ uint32(c)) * 16777619
	}
	return hash % namePrefixBits
}

// nameEndTrimmed returns the length of a folded name without what follows it in a sentence:
// the full stop of "Anna Real." or the possessive of "Anna Real's".
func nameEndTrimmed(name []byte) int {
	end := len(name)
	tail := string(name[max(0, end-4):])
	if strings.HasSuffix(tail, "'s") {
		end -= 2
	} else if strings.HasSuffix(tail, string(typographicApostrophe)+"s") {
		end -= 4
	}
	for end > 0 {
		if name[end-1] == '.' || name[end-1] == '\'' || name[end-1] == '-' {
			end--
		} else if end >= 3 && string(name[end-3:end]) == string(typographicApostrophe) {
			end -= 3
		} else {
			break
		}
	}
	return end
}

// nameKey hashes a name in the form a text lookup produces: lower case, words separated
// by one space, nothing trailing. It rejects what cannot be a name, such as anything with a
// digit.
func (d *nameDictionary) nameKey(name []byte) (uint64, namePrefixes, bool) {
	var buffer [nameMaxBytes]byte
	length, words, wordLength, lastWord := 0, 0, 0, 0
	for i := 0; i < len(name); {
		if gap := nameGapLen(name, i); gap > 0 {
			if wordLength > 0 {
				if wordLength < 2 && words == 0 {
					return 0, namePrefixes{}, false
				}
				words++
				lastWord, wordLength = wordLength, 0
			}
			i += gap
			continue
		}
		folded, width := nameCharAt(name, i)
		if width == 0 {
			return 0, namePrefixes{}, false
		}
		if wordLength == 0 && length > 0 {
			if length == len(buffer) {
				return 0, namePrefixes{}, false
			}
			buffer[length] = ' '
			length++
		}
		if length+width > len(buffer) {
			return 0, namePrefixes{}, false
		}
		copy(buffer[length:], folded[:width])
		length += width
		wordLength += width
		i += width
	}
	if wordLength > 0 {
		words++
		lastWord = wordLength
	}
	if words < 2 || words > nameMaxWords || lastWord < 2 || length < 5 {
		return 0, namePrefixes{}, false
	}
	folded := buffer[:nameEndTrimmed(buffer[:length])]
	firstEnd := bytes.IndexByte(folded, ' ')
	twoEnd := len(folded)
	if second := bytes.IndexByte(folded[firstEnd+1:], ' '); second >= 0 {
		twoEnd = firstEnd + 1 + second
	}
	return maphash.Bytes(d.seed, folded), namePrefixes{
		first:    normalizedPrefixHash(folded[:firstEnd]),
		firstTwo: normalizedPrefixHash(folded[:nameEndTrimmed(folded[:twoEnd])]),
	}, true
}

var nameBytes = buildNameBytes()

func buildNameBytes() [256]bool {
	var set [256]bool
	for c := 0; c < utf8.RuneSelf; c++ {
		b := byte(c)
		set[c] = b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '-' || b == '\'' || b == '.'
	}
	return set
}

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// piiClass is what a column, meta key, or structured key says about its value.
type piiClass uint8

const (
	piiNone piiClass = iota
	piiSecret
	piiPhone
	piiFirstName
	piiLastName
	piiFullName
	piiLogin
	piiStreet
	piiCity
	piiPostcode
	piiCompany
	piiBirthdate
	piiIP
	piiUserAgent
	piiIdentifier
	// piiVersion is no personal data: it spares a value from the scan for numbers.
	piiVersion
)

// credentialPattern matches settings that hold a secret, anchored at the end so that an
// option such as woocommerce_myaccount_lost_password_endpoint keeps its value.
var credentialPattern = regexp.MustCompile(credentialKeys)

var piiKeyNames = map[string]piiClass{
	"phone": piiPhone, "phone_number": piiPhone, "phonenumber": piiPhone, "telephone": piiPhone, "telefon": piiPhone,
	"telefonnummer": piiPhone, "tel": piiPhone, "fax": piiPhone, "mobile": piiPhone, "mobile_phone": piiPhone,
	"mobile_number": piiPhone, "cell": piiPhone, "cellphone": piiPhone, "cell_phone": piiPhone, "handy": piiPhone,
	"whatsapp": piiPhone, "mobil": piiPhone, "mobilnummer": piiPhone,

	"first_name": piiFirstName, "firstname": piiFirstName, "fname": piiFirstName, "given_name": piiFirstName,
	"givenname": piiFirstName, "forename": piiFirstName, "vorname": piiFirstName,
	"last_name": piiLastName, "lastname": piiLastName, "lname": piiLastName, "surname": piiLastName,
	"family_name": piiLastName, "familyname": piiLastName, "nachname": piiLastName,
	"full_name": piiFullName, "fullname": piiFullName, "display_name": piiFullName, "displayname": piiFullName,
	"customer_name": piiFullName, "client_name": piiFullName, "contact_name": piiFullName, "billing_name": piiFullName,
	"shipping_name": piiFullName, "author_name": piiFullName, "sender_name": piiFullName, "recipient_name": piiFullName,
	"member_name": piiFullName, "person_name": piiFullName, "your_name": piiFullName, "cardholder": piiFullName,
	"cardholder_name": piiFullName, "card_name": piiFullName, "account_holder": piiFullName, "name_on_card": piiFullName,

	"username": piiLogin, "user_name": piiLogin, "user_login": piiLogin, "login_name": piiLogin, "benutzername": piiLogin,

	"street": piiStreet, "strasse": piiStreet, "straße": piiStreet, "street_address": piiStreet, "address": piiStreet,
	"address1": piiStreet, "address_1": piiStreet, "address2": piiStreet, "address_2": piiStreet, "adresse": piiStreet,
	"address_line_1": piiStreet, "address_line_2": piiStreet, "house_number": piiStreet, "hausnummer": piiStreet,
	"postal_address": piiStreet, "home_address": piiStreet,

	"city": piiCity, "town": piiCity, "stadt": piiCity, "wohnort": piiCity,
	"postcode": piiPostcode, "post_code": piiPostcode, "postal_code": piiPostcode, "postalcode": piiPostcode,
	"zip": piiPostcode, "zipcode": piiPostcode, "zip_code": piiPostcode, "plz": piiPostcode, "postleitzahl": piiPostcode,
	"company": piiCompany, "company_name": piiCompany, "firma": piiCompany, "firmenname": piiCompany,

	"birth": piiBirthdate, "birthday": piiBirthdate, "birthdate": piiBirthdate, "birth_date": piiBirthdate,
	"date_of_birth": piiBirthdate, "dob": piiBirthdate, "geburtstag": piiBirthdate, "geburtsdatum": piiBirthdate,

	"ip": piiIP, "ip_address": piiIP, "ipaddress": piiIP, "ip_addr": piiIP, "client_ip": piiIP, "remote_ip": piiIP,
	"remote_addr": piiIP, "user_ip": piiIP, "customer_ip": piiIP, "customer_ip_address": piiIP,

	"user_agent": piiUserAgent, "useragent": piiUserAgent, "http_user_agent": piiUserAgent, "customer_user_agent": piiUserAgent,

	"iban": piiIdentifier, "bic": piiIdentifier, "swift": piiIdentifier, "bank_account": piiIdentifier,
	"account_number": piiIdentifier, "routing_number": piiIdentifier, "sort_code": piiIdentifier, "card_number": piiIdentifier,
	"cc_number": piiIdentifier, "credit_card": piiIdentifier, "cvv": piiIdentifier, "cvc": piiIdentifier, "ssn": piiIdentifier,
	"social_security": piiIdentifier, "social_security_number": piiIdentifier, "passport": piiIdentifier,
	"passport_number": piiIdentifier, "tax_id": piiIdentifier, "tax_number": piiIdentifier, "vat_id": piiIdentifier,
	"vat_number": piiIdentifier, "ustid": piiIdentifier, "ust_id": piiIdentifier, "steuernummer": piiIdentifier,
	"national_id": piiIdentifier, "personalausweis": piiIdentifier, "gender": piiIdentifier, "geschlecht": piiIdentifier,
	"nationality": piiIdentifier, "religion": piiIdentifier,
}

// piiKeyScopes are prefixes that say whose value it is, not what it is.
var piiKeyScopes = []string{"billing_", "shipping_", "customer_", "user_", "contact_", "client_", "member_", "my_", "your_", "sender_", "recipient_", "person_", "author_"}

// piiKeyTails end a key that describes a field rather than holding its value, as in
// billing_phone_label or a form's phone_required setting.
var piiKeyTails = map[string]bool{
	"id": true, "ids": true, "uuid": true, "key": true, "keys": true, "type": true, "types": true, "format": true,
	"label": true, "labels": true, "field": true, "fields": true, "placeholder": true, "enabled": true, "enable": true,
	"disabled": true, "required": true, "show": true, "hide": true, "visible": true, "visibility": true, "count": true,
	"status": true, "template": true, "class": true, "classes": true, "style": true, "color": true, "size": true,
	"width": true, "height": true, "option": true, "options": true, "setting": true, "settings": true, "text": true,
	"title": true, "description": true, "desc": true, "message": true, "error": true, "errors": true, "link": true,
	"icon": true, "prefix": true, "suffix": true, "mode": true, "position": true, "order": true, "sort": true,
	"min": true, "max": true, "length": true, "pattern": true, "regex": true, "validation": true, "column": true,
	"columns": true, "breakpoint": true, "menu": true,
}

// piiKeyHeads start a key that switches something, as in show_phone or is_mobile.
var piiKeyHeads = map[string]bool{
	"show": true, "hide": true, "enable": true, "disable": true, "is": true, "has": true, "require": true,
	"required": true, "use": true, "allow": true, "display": true, "no": true, "tax": true, "default": true,
}

// classifyKey says what a name promises about its value. Names are matched as whole
// tokens, so address_id or capacity never count, and any table can run through this: a
// plugin's phone column gets the same treatment as a phone key inside serialized data.
func classifyKey(name string) piiClass {
	if credentialPattern.MatchString(name) {
		return piiSecret
	}
	tokens := splitKeyTokens(name)
	if len(tokens) == 0 {
		return piiNone
	}
	joined := strings.Join(tokens, "_")
	if joined == "version" || tokens[len(tokens)-1] == "version" || tokens[len(tokens)-1] == "ver" {
		return piiVersion
	}
	// An exact name says what it says, even with a head or tail: display_name, vat_id.
	if class, ok := piiKeyNames[joined]; ok {
		return class
	}
	if piiKeyHeads[tokens[0]] || piiKeyTails[tokens[len(tokens)-1]] {
		return piiNone
	}
	for _, token := range tokens {
		if token == "email" || token == "mail" || token == "e_mail" {
			// The address scan covers these, and an email_address is no postal address.
			return piiNone
		}
	}
	for _, scope := range piiKeyScopes {
		if rest, ok := strings.CutPrefix(joined, scope); ok {
			if class, ok := piiKeyNames[rest]; ok {
				return class
			}
		}
	}
	if len(tokens) > 1 {
		if class, ok := piiKeyNames[tokens[len(tokens)-1]]; ok && class == piiPhone {
			return class
		}
	}
	return piiNone
}

// splitKeyTokens lowers a name and splits it on anything that is not a letter or digit,
// and at camelCase humps. A serialized private property such as "\0*\0phone" ends up the
// same as a plain one.
func splitKeyTokens(name string) []string {
	tokens := make([]string, 0, 4)
	current := make([]byte, 0, len(name))
	flush := func() {
		if len(current) > 0 {
			tokens = append(tokens, string(current))
			current = current[:0]
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			if i > 0 && name[i-1] >= 'a' && name[i-1] <= 'z' {
				flush()
			}
			current = append(current, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c >= 0x80:
			current = append(current, c)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func isBooleanLike(value []byte) bool {
	if len(value) > 5 {
		return false
	}
	switch strings.ToLower(string(value)) {
	case "", "0", "1", "on", "off", "yes", "no", "true", "false", "null":
		return true
	}
	return false
}

func containsDigits(value []byte, count int) bool {
	found := 0
	for _, c := range value {
		if c >= '0' && c <= '9' {
			found++
			if found >= count {
				return true
			}
		}
	}
	return false
}

func containsLetter(value []byte) bool {
	for _, c := range value {
		if isASCIILetter(c) || c >= 0x80 {
			return true
		}
	}
	return false
}

// neutralChildKeys hold no personal data even under a personal key: {"address":{"country":"DE"}}.
var neutralChildKeys = map[string]bool{"country": true, "country_code": true, "state": true, "region": true}

// childClass is what a key inside a structure says, falling back on the key the structure
// itself sits under: {"phone":["0170…"]} and {"address":{"line1":"…"}} name their values
// no less clearly than a flat key would.
func (v *valueScanner) childClass(parent piiClass, key []byte) piiClass {
	class := v.classify(key)
	if class != piiNone || parent == piiNone || parent == piiVersion {
		return class
	}
	tokens := splitKeyTokens(string(key))
	if len(tokens) == 0 || piiKeyTails[tokens[len(tokens)-1]] || neutralChildKeys[strings.Join(tokens, "_")] {
		return piiNone
	}
	return parent
}

// redactClass returns the placeholder for a value its key calls personal, or false when
// the value does not look like what the key promises. A form's "phone": "1" or a label
// such as "first_name": "{{name}}" stays.
func (v *valueScanner) redactClass(class piiClass, value []byte) ([]byte, bool) {
	if class == piiNone || class == piiVersion || isBooleanLike(value) {
		return nil, false
	}
	if _, kept := v.scanner.keep[string(value)]; kept {
		return nil, false
	}
	if value[0] == '{' || value[0] == '[' || value[0] == '%' {
		return nil, false
	}
	switch class {
	case piiSecret:
		return []byte{}, true
	case piiPhone:
		if containsDigits(value, 5) {
			return []byte(placeholderPhone), true
		}
	case piiFirstName, piiLastName, piiFullName, piiLogin:
		if !containsLetter(value) || len(value) > 200 {
			return nil, false
		}
		identity := v.scanner.registry.forRow("name:" + strings.ToLower(string(value)))
		switch class {
		case piiFirstName:
			return identity.appendFirstName(nil), true
		case piiLastName:
			return identity.appendLastName(nil), true
		case piiLogin:
			return identity.appendSlug(nil), true
		}
		return identity.appendDisplayName(nil), true
	case piiStreet:
		if containsLetter(value) || containsDigits(value, 1) {
			return []byte(placeholderStreet), true
		}
	case piiCity:
		if containsLetter(value) {
			return []byte(placeholderCity), true
		}
	case piiPostcode:
		if containsDigits(value, 3) {
			return []byte(placeholderPostcode), true
		}
	case piiCompany:
		if containsLetter(value) {
			return []byte(placeholderCompany), true
		}
	case piiBirthdate:
		if containsDigits(value, 2) {
			return []byte(placeholderBirthdate), true
		}
	case piiIP:
		if containsDigits(value, 2) && bytes.ContainsAny(value, ".:") {
			if bytes.IndexByte(value, ':') >= 0 {
				return []byte(placeholderIPv6), true
			}
			return []byte(placeholderIPv4), true
		}
	case piiUserAgent:
		return []byte(placeholderUserAgent), true
	case piiIdentifier:
		return []byte(placeholderRedacted), true
	}
	return nil, false
}

// valueScanner decides how to read a stored value: serialized PHP, JSON, or plain text.
// Structure matters because of what it says about a value: the key "phone" tells more
// than any pattern, and serialized lengths must follow every change.
type valueScanner struct {
	scanner *piiScanner
	// keys caches classifyKey. A site has few distinct keys over millions of values.
	keys map[string]piiClass
}

const structuredDepthLimit = 8

func (v *valueScanner) classify(key []byte) piiClass {
	if class, ok := v.keys[string(key)]; ok {
		return class
	}
	class := classifyKey(string(key))
	if len(v.keys) < 1<<16 {
		v.keys[string(key)] = class
	}
	return class
}

// scanValue returns the value itself when nothing changed.
func (v *valueScanner) scanValue(value []byte, depth int) []byte {
	if len(value) < 3 {
		return value
	}
	if depth < structuredDepthLimit {
		if looksSerialized(value) {
			if out, err := v.scanSerialized(value, depth); err == nil {
				return out
			}
			// Text with this start that does not parse is no valid serialization, so a
			// plain scan cannot break it any further.
		} else if first := value[0]; (first == '{' || first == '[') && (value[len(value)-1] == '}' || value[len(value)-1] == ']') && json.Valid(value) {
			return v.scanJSON(value, depth)
		}
	}
	return v.scanner.scan(value)
}

func looksSerialized(value []byte) bool {
	if len(value) < 4 || value[1] != ':' {
		return false
	}
	switch value[0] {
	case 'a', 'O', 's', 'C':
	default:
		return false
	}
	i := 2
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 2 || i+1 >= len(value) || value[i] != ':' {
		return false
	}
	if value[0] == 'a' {
		return value[i+1] == '{'
	}
	return value[i+1] == '"'
}

// scanKey handles a key: its name stays, except for an address in it.
func (v *valueScanner) scanKey(key []byte) []byte {
	only := v.scanner.emailsOnly
	v.scanner.emailsOnly = true
	out := v.scanner.scan(key)
	v.scanner.emailsOnly = only
	return out
}

// scanClassified applies a key's verdict to a string value, then scans what is left.
func (v *valueScanner) scanClassified(class piiClass, value []byte, depth int) []byte {
	if !v.scanner.emailsOnly {
		if replacement, ok := v.redactClass(class, value); ok {
			v.scanner.other++
			return replacement
		}
	}
	if class == piiVersion {
		skip := v.scanner.skipNumbers
		v.scanner.skipNumbers = true
		out := v.scanValue(value, depth+1)
		v.scanner.skipNumbers = skip
		return out
	}
	return v.scanValue(value, depth+1)
}

// --- serialized PHP ----------------------------------------------------------------

var errNotSerialized = errors.New("not serialized")

// serializedWalker parses serialized PHP without unserialize semantics: nothing is
// instantiated, and it knows nothing of the classes involved. It copies only when a value
// changes, so a settings blob without personal data costs one read.
type serializedWalker struct {
	v      *valueScanner
	data   []byte
	pos    int
	out    []byte
	copied int
	depth  int
}

func (v *valueScanner) scanSerialized(value []byte, depth int) ([]byte, error) {
	walker := serializedWalker{v: v, data: value, depth: depth}
	if err := walker.value(piiNone, 0); err != nil {
		return nil, err
	}
	// A trailing newline is common enough in hand-edited values and harmless to PHP.
	if len(bytes.TrimRight(value[walker.pos:], " \t\r\n")) != 0 {
		return nil, errNotSerialized
	}
	if walker.out == nil {
		return value, nil
	}
	return append(walker.out, value[walker.copied:]...), nil
}

func (w *serializedWalker) replace(start int, end int, rendered []byte) {
	if w.out == nil {
		w.out = make([]byte, 0, len(w.data)+32)
	}
	w.out = append(w.out, w.data[w.copied:start]...)
	w.out = append(w.out, rendered...)
	w.copied = end
}

// length reads digits up to the delimiter, moving past it.
func (w *serializedWalker) length(delimiter byte) (int, error) {
	start := w.pos
	for w.pos < len(w.data) && w.data[w.pos] >= '0' && w.data[w.pos] <= '9' {
		w.pos++
	}
	if w.pos == start || w.pos >= len(w.data) || w.data[w.pos] != delimiter || w.pos-start > 10 {
		return 0, errNotSerialized
	}
	value, err := strconv.Atoi(string(w.data[start:w.pos]))
	if err != nil {
		return 0, errNotSerialized
	}
	w.pos++
	return value, nil
}

func (w *serializedWalker) expect(c byte) error {
	if w.pos >= len(w.data) || w.data[w.pos] != c {
		return errNotSerialized
	}
	w.pos++
	return nil
}

// quoted reads a length-prefixed "…" body, returning its bounds.
func (w *serializedWalker) quoted() (int, int, error) {
	length, err := w.length(':')
	if err != nil {
		return 0, 0, err
	}
	if err := w.expect('"'); err != nil {
		return 0, 0, err
	}
	start := w.pos
	if length > len(w.data)-start-1 {
		return 0, 0, errNotSerialized
	}
	w.pos = start + length
	if err := w.expect('"'); err != nil {
		return 0, 0, err
	}
	return start, start + length, nil
}

// stringToken reads s:N:"…"; with a transform, rewriting its length when the body changes.
func (w *serializedWalker) stringToken(transform func([]byte) []byte) error {
	tokenStart := w.pos
	w.pos += 2
	start, end, err := w.quoted()
	if err != nil {
		return err
	}
	if err := w.expect(';'); err != nil {
		return err
	}
	body := w.data[start:end]
	changed := transform(body)
	if bytes.Equal(changed, body) {
		return nil
	}
	rendered := make([]byte, 0, len(changed)+16)
	rendered = append(rendered, 's', ':')
	rendered = strconv.AppendInt(rendered, int64(len(changed)), 10)
	rendered = append(rendered, ':', '"')
	rendered = append(rendered, changed...)
	rendered = append(rendered, '"', ';')
	w.replace(tokenStart, w.pos, rendered)
	return nil
}

func (w *serializedWalker) pairs(count int, nesting int, parent piiClass) error {
	if err := w.expect('{'); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		class, err := w.key(parent)
		if err != nil {
			return err
		}
		if err := w.value(class, nesting+1); err != nil {
			return err
		}
	}
	return w.expect('}')
}

func (w *serializedWalker) key(parent piiClass) (piiClass, error) {
	if w.pos+1 >= len(w.data) || w.data[w.pos+1] != ':' {
		return piiNone, errNotSerialized
	}
	switch w.data[w.pos] {
	case 'i':
		w.pos += 2
		return parent, w.skipTo(';')
	case 's':
		class := piiNone
		err := w.stringToken(func(key []byte) []byte {
			class = w.v.childClass(parent, key)
			return w.v.scanKey(key)
		})
		return class, err
	}
	return piiNone, errNotSerialized
}

func (w *serializedWalker) skipTo(delimiter byte) error {
	index := bytes.IndexByte(w.data[w.pos:], delimiter)
	if index < 0 {
		return errNotSerialized
	}
	w.pos += index + 1
	return nil
}

func (w *serializedWalker) value(class piiClass, nesting int) error {
	if nesting > 64 || w.pos >= len(w.data) {
		return errNotSerialized
	}
	switch w.data[w.pos] {
	case 'N':
		w.pos++
		return w.expect(';')
	}
	if w.pos+1 >= len(w.data) || w.data[w.pos+1] != ':' {
		return errNotSerialized
	}
	tokenStart := w.pos
	switch w.data[w.pos] {
	case 's':
		return w.stringToken(func(body []byte) []byte {
			if class == piiNone {
				return w.v.scanValue(body, w.depth+1)
			}
			return w.v.scanClassified(class, body, w.depth)
		})
	case 'i', 'd':
		w.pos += 2
		if err := w.skipTo(';'); err != nil {
			return err
		}
		// A number under a personal key is the value itself, such as a phone without a +.
		if class != piiNone && class != piiVersion && !w.v.scanner.emailsOnly && len(w.data[tokenStart:w.pos]) > 8 {
			w.v.scanner.other++
			w.replace(tokenStart, w.pos, []byte("i:0;"))
		}
		return nil
	case 'b', 'r', 'R':
		w.pos += 2
		return w.skipTo(';')
	case 'E':
		w.pos += 2
		if _, _, err := w.quoted(); err != nil {
			return err
		}
		return w.expect(';')
	case 'a':
		w.pos += 2
		count, err := w.length(':')
		if err != nil {
			return err
		}
		return w.pairs(count, nesting, class)
	case 'O':
		w.pos += 2
		if _, _, err := w.quoted(); err != nil {
			return err
		}
		if err := w.expect(':'); err != nil {
			return err
		}
		count, err := w.length(':')
		if err != nil {
			return err
		}
		return w.pairs(count, nesting, class)
	case 'C':
		return w.customObject(tokenStart)
	}
	return errNotSerialized
}

// customObject reads C:…:"Class":N:{payload}. The payload's format is the class's own, so
// it gets a text scan, and its length follows the change.
func (w *serializedWalker) customObject(tokenStart int) error {
	w.pos += 2
	classStart, classEnd, err := w.quoted()
	if err != nil {
		return err
	}
	if err := w.expect(':'); err != nil {
		return err
	}
	length, err := w.length(':')
	if err != nil {
		return err
	}
	if err := w.expect('{'); err != nil {
		return err
	}
	start := w.pos
	if length > len(w.data)-start-1 {
		return errNotSerialized
	}
	w.pos = start + length
	if err := w.expect('}'); err != nil {
		return err
	}
	payload := w.data[start : start+length]
	changed := w.v.scanner.scan(payload)
	if bytes.Equal(changed, payload) {
		return nil
	}
	name := w.data[classStart:classEnd]
	rendered := []byte("C:" + strconv.Itoa(len(name)) + ":\"" + string(name) + "\":" + strconv.Itoa(len(changed)) + ":{")
	rendered = append(rendered, changed...)
	rendered = append(rendered, '}')
	w.replace(tokenStart, w.pos, rendered)
	return nil
}

// --- JSON --------------------------------------------------------------------------

// scanJSON walks valid JSON token by token rather than decoding it, so that key order,
// number formats, and escapes stay as they are for everything that does not change. A
// stack keeps the key each object or array sits under.
func (v *valueScanner) scanJSON(data []byte, depth int) []byte {
	var out []byte
	copied, i := 0, 0
	replace := func(start int, end int, rendered []byte) {
		if out == nil {
			out = make([]byte, 0, len(data)+32)
		}
		out = append(out, data[copied:start]...)
		out = append(out, rendered...)
		copied = end
	}
	stack := make([]piiClass, 0, 8)
	current, pending := piiNone, piiNone
	for i < len(data) {
		switch c := data[i]; {
		case c == '{' || c == '[':
			stack = append(stack, current)
			current, pending = pending, piiNone
			// Inside the structure, values take the class of the key it sits under.
			pending = current
			i++
		case c == '}' || c == ']':
			if len(stack) > 0 {
				current = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			pending = current
			i++
		case c == '"':
			end := jsonStringEnd(data, i+1)
			body := data[i+1 : end]
			next := skipJSONSpace(data, end+1)
			if next < len(data) && data[next] == ':' {
				if changed := v.scanKey(body); !bytes.Equal(changed, body) {
					replace(i+1, end, changed)
				}
				pending = v.childClass(current, body)
				i = next + 1
				continue
			}
			if changed := v.scanJSONString(body, pending, depth); !bytes.Equal(changed, body) {
				replace(i+1, end, changed)
			}
			pending = current
			i = end + 1
		case c == '-' || c >= '0' && c <= '9':
			end := i + 1
			for end < len(data) && (data[end] >= '0' && data[end] <= '9' || data[end] == '.') {
				end++
			}
			if !v.scanner.emailsOnly && pending != piiNone && pending != piiVersion && end-i >= 5 {
				v.scanner.other++
				replace(i, end, []byte("0"))
			}
			pending = current
			i = end
		case c == ',':
			pending = current
			i++
		default:
			i++
		}
	}
	if out == nil {
		return data
	}
	return append(out, data[copied:]...)
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
		i++
	}
	return i
}

func jsonStringEnd(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i
		}
		i++
	}
	return len(data) - 1
}

// scanJSONString handles the raw body of a string. One with escapes gets decoded first:
// the n of a line break would read as a letter, and structure inside the string has lengths
// and keys that only mean something unescaped. Only a changed string gets encoded again.
func (v *valueScanner) scanJSONString(raw []byte, class piiClass, depth int) []byte {
	if len(raw) == 0 {
		return raw
	}
	text := raw
	escaped := bytes.IndexByte(raw, '\\') >= 0
	if escaped {
		quoted := make([]byte, 0, len(raw)+2)
		quoted = append(append(append(quoted, '"'), raw...), '"')
		decoded := ""
		if err := json.Unmarshal(quoted, &decoded); err != nil {
			return v.scanner.scan(raw)
		}
		text = []byte(decoded)
	}
	changed := v.scanClassified(class, text, depth)
	if bytes.Equal(changed, text) {
		return raw
	}
	if !escaped && !needsJSONEscape(changed) {
		return changed
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(string(changed)); err != nil {
		return v.scanner.scan(raw)
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	return encoded[1 : len(encoded)-1]
}

func needsJSONEscape(value []byte) bool {
	for _, c := range value {
		if c == '"' || c == '\\' || c < 0x20 {
			return true
		}
	}
	return false
}
